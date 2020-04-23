package goocp

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/shaoyuan1943/gokcp"
)

var convID uint32 = 1000
var timeScheduler *TimerScheduler = NewTimerScheduler()

type Session struct {
	id           uint32
	rwc          net.PacketConn
	remoteAddr   net.Addr
	kcp          *gokcp.KCP
	sc           *ServerConn
	state        ClientConnState
	err          error
	writeNotifer chan struct{}
	outPackets   [][]byte
	ctx          context.Context
	quitF        context.CancelFunc
	mx           sync.Mutex
}

func newSessionFromServer(sc *ServerConn, conn net.PacketConn, addr net.Addr, client bool) *Session {
	return newSession(sc, conn, addr, nil)
}

func newSession(sc *ServerConn, conn net.PacketConn, addr net.Addr, state ClientConnState) *Session {
	s := &Session{}
	s.id = atomic.AddUint32(&convID, 1)
	s.sc = sc
	s.rwc = conn
	s.remoteAddr = addr
	s.state = state
	s.ctx, s.quitF = context.WithCancel(context.Background())
	s.writeNotifer = make(chan struct{}, 1)
	s.kcp = gokcp.NewKCP(s.id, s.dataOut)
	return s
}

func (s *Session) ID() uint32 {
	return s.id
}

func (s *Session) IsClient() bool {
	return s.sc == nil
}

func (s *Session) SetClientConnState(state ClientConnState) {
	if state != nil {
		s.state = state
	}
}

func (s *Session) Close(err error) {
	s.err = err
	if s.sc != nil {
		s.sc.notifyErrSession(s)
	}
}

func (s *Session) onKCPDataComing(data []byte) error {
	if len(data) == 0 || len(data) < int(gokcp.KCP_OVERHEAD) {
		return errors.New("invalid recv data")
	}

	return s.kcp.Input(data)
}

func (s *Session) dataOut(data []byte) {
	if len(data) == 0 || len(data) < int(gokcp.KCP_OVERHEAD) {
		return
	}

	_, err := s.rwc.WriteTo(data, s.remoteAddr)
	if err != nil {
		s.Close(err)
	}
}

func (s *Session) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, errors.New("invalid data")
	}

	s.mx.Lock()
	s.outPackets = append(s.outPackets, data)
	s.mx.Unlock()

	select {
	case s.writeNotifer <- struct{}{}:
	default:
	}

	return len(data), nil
}

func (s *Session) loop() {
	readedBuffer := make([]byte, gokcp.KCP_MTU_DEF+150)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.writeNotifer:
			waitSend := s.kcp.WaitSend()
			if waitSend < int(s.kcp.SendWnd()) && waitSend < int(s.kcp.RemoteWnd()) {
				s.mx.Lock()
				var outPackets [][]byte
				outPackets = append(outPackets, s.outPackets...)
				s.outPackets = s.outPackets[:0]
				s.mx.Unlock()

				for idx := range outPackets {
					data := outPackets[idx]
					err := s.kcp.Send(data)
					if err != nil {
						if s.state != nil {
							s.state.OnSendDataError(data, err)
						}
					}
				}
			}
		default:
			if !s.kcp.IsStreamMode() {
				if size := s.kcp.PeekSize(); size > 0 {
					n, err := s.kcp.Recv(readedBuffer)
					if err != nil {
						if err == gokcp.ErrNoEnoughSpace {
							readedBuffer = make([]byte, size)
							break
						}
					} else {
						if s.state != nil {
							s.state.OnNewDataComing(readedBuffer[:n])
						}
					}
				}
			} else {
				for {
					if size := s.kcp.PeekSize(); size > 0 {
						n, err := s.kcp.Recv(readedBuffer)
						if err == nil && s.state != nil {
							s.state.OnNewDataComing(readedBuffer[:n])
						} else {
							break
						}
					} else {
						break
					}
				}
			}
		}
	}
}

func (s *Session) update() {
	select {
	case <-s.ctx.Done():
		return
	default:
	}

	s.kcp.Update()
	nextTime := s.kcp.Check()
	timeScheduler.PushTask(s.update, nextTime)
}

func Dial(addr string, state ClientConnState) (*Session, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}

	network := "udp4"
	if udpAddr.IP.To4() == nil {
		network = "udp6"
	}

	udpConn, err := net.ListenUDP(network, nil)
	if err != nil {
		return nil, err
	}

	s := newSession(nil, udpConn, udpAddr, state)
	go s.loop()

	timeScheduler.PushTask(s.update, 1)
	return s, nil
}
