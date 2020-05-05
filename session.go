package goocp

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"net"
	"sync"
	"sync/atomic"

	"github.com/shaoyuan1943/gokcp"
)

var convID uint32 = 1000
var timerScheduler *TimerScheduler = NewTimerScheduler()

type Session struct {
	id           uint32
	rwc          net.PacketConn
	remoteAddr   net.Addr
	kcp          *gokcp.KCP
	sc           *ServerConn
	stateHandler ClientConnState
	writeNotifer chan struct{}
	outPackets   [][]byte
	closedC      chan struct{}
	mx           sync.RWMutex
	closeOnce    sync.Once
	codec        PacketCrypto
}

func newSessionFromServer(convID uint32, sc *ServerConn, conn net.PacketConn, addr net.Addr) *Session {
	return newSession(convID, sc, conn, addr, nil)
}

func newSession(convID uint32, sc *ServerConn, conn net.PacketConn, addr net.Addr, stateHandler ClientConnState) *Session {
	s := &Session{}
	s.id = convID
	s.sc = sc
	s.rwc = conn
	s.remoteAddr = addr
	s.stateHandler = stateHandler
	s.closedC = make(chan struct{})
	s.writeNotifer = make(chan struct{}, 1)
	s.kcp = gokcp.NewKCP(s.id, s.dataOutput)
	s.kcp.SetBufferReserved(int(HEADER_SIZE))
	s.kcp.SetNoDelay(true, 10, 2, true)

	// to save goroutines
	timerScheduler.PushTask(s.update, gokcp.CurrentMS())
	go s.writeLoop()
	go s.readLoop()

	return s
}

func (s *Session) SetEncryptoCodec(codec PacketCrypto) {
	if codec != nil {
		s.codec = codec
	}
}

func (s *Session) ID() uint32 {
	return s.id
}

func (s *Session) IsClient() bool {
	return s.sc == nil
}

func (s *Session) SetClientConnStateHandler(stateHandler ClientConnState) {
	if stateHandler != nil {
		s.stateHandler = stateHandler
	}
}

func (s *Session) handleClose(err error) {
	s.close(err, false)
}

func (s *Session) Close(err error) {
	s.close(err, true)
}

func (s *Session) close(err error, sendClose bool) {
	if s.rwc == nil {
		return
	}

	s.closeOnce.Do(func() {
		close(s.closedC)

		if sendClose {
			// send disconnect, ignore return error
			data := make([]byte, int(gokcp.KCP_OVERHEAD)+int(HEADER_SIZE))
			s.encodeData(data, HEADER_CMD_DISCONNECT)
			s.rwc.WriteTo(data, s.remoteAddr)
		}

		if s.IsClient() {
			s.rwc.Close()
		}

		if s.stateHandler != nil {
			s.stateHandler.OnClosed(err)
		}

		if !s.IsClient() {
			s.sc.notifyErrSession(s)
		}
	})
}

func (s *Session) onKCPDataComing(data []byte) error {
	if len(data) == 0 || len(data) < int(gokcp.KCP_OVERHEAD) {
		return errors.New("invalid recv data")
	}

	s.mx.Lock()
	defer s.mx.Unlock()

	return s.kcp.Input(data)
}

func (s *Session) encodeData(data []byte, cmd uint16) error {
	binary.LittleEndian.PutUint16(data[12:], cmd)
	sum := crc32.ChecksumIEEE(data[14:])
	binary.LittleEndian.PutUint32(data[8:], sum)

	if s.codec != nil {
		return s.codec.Encrypto(data, data)
	}

	return nil
}

func (s *Session) dataOutput(data []byte) {
	if len(data) == 0 || len(data) < int(gokcp.KCP_OVERHEAD) {
		return
	}

	err := s.encodeData(data, HEADER_CMD_DATA)
	if err != nil {
		s.stateHandler.OnSendDataError(data, err)
		return
	}

	_, err = s.rwc.WriteTo(data, s.remoteAddr)
	if err != nil {
		// if has error, kcp will resend this data in next update time
		// TODO:?
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

func (s *Session) readLoop() {
	if !s.IsClient() {
		return
	}

	buffer := make([]byte, s.kcp.Mtu()+150)
	for {
		select {
		case <-s.closedC:
			return
		default:
			n, addr, err := s.rwc.ReadFrom(buffer)
			if err != nil {
				s.close(err, true)
				return
			}

			if addr.String() != s.remoteAddr.String() {
				s.close(errors.New("different remote addr"), false)
				return
			}

			err = s.handleData(buffer[:n])
			if err != nil {
				// if has error, remote kcp will resend data
			}
		}
	}
}

func (s *Session) decodeData(data []byte) ([]byte, error) {
	if s.codec != nil {
		// 1. encrypto
		err := s.codec.Decrypto(data, data)
		if err != nil {
			return nil, err
		}
	}

	// 2. check logic data(include kcp header) sum
	sum1 := crc32.ChecksumIEEE(data[14:])
	sum2 := binary.LittleEndian.Uint32(data[8:])
	if sum1 != sum2 {
		return nil, errors.New("different data sum")
	}

	return data[12:], nil
}

func (s *Session) handleData(buffer []byte) error {
	data, err := s.decodeData(buffer)
	if err != nil {
		return err
	}

	// 3. check data cmd
	cmd := binary.LittleEndian.Uint16(data)
	if cmd == HEADER_CMD_DISCONNECT {
		s.close(errors.New("remote connection closed"), false)
		return nil
	} else if cmd == HEADER_CMD_DATA {
		data = data[2:]
		convID := binary.LittleEndian.Uint32(data)
		if convID == 0 {
			return errors.New("invalid kcp data")
		}

		if s.kcp.ConvID() != convID {
			return errors.New("unknow conversation ID")
		}

		return s.onKCPDataComing(data)
	} else {
		return errors.New("unknow data cmd")
	}

	return nil
}

func (s *Session) writeLoop() {
	readedBuffer := make([]byte, gokcp.KCP_MTU_DEF+150)
	for {
		select {
		case <-s.closedC:
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
					s.mx.Lock()
					err := s.kcp.Send(data)
					s.mx.Unlock()
					if err != nil {
						if s.stateHandler != nil {
							s.stateHandler.OnSendDataError(data, err)
						}
					}
				}
			}
		default:
			if !s.kcp.IsStreamMode() {
				if size := s.kcp.PeekSize(); size > 0 {
					s.mx.Lock()
					n, err := s.kcp.Recv(readedBuffer)
					s.mx.Unlock()
					if err != nil {
						if err == gokcp.ErrNoEnoughSpace {
							readedBuffer = make([]byte, size)
							break
						}
					} else {
						if s.stateHandler != nil {
							s.stateHandler.OnNewDataComing(readedBuffer[:n])
						}
					}
				}
			} else {
				for {
					if size := s.kcp.PeekSize(); size > 0 {
						s.mx.Lock()
						n, err := s.kcp.Recv(readedBuffer)
						s.mx.Unlock()
						if err == nil && s.stateHandler != nil {
							s.stateHandler.OnNewDataComing(readedBuffer[:n])
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
	case <-s.closedC:
		return
	default:
	}

	s.mx.Lock()
	s.kcp.Update()
	s.mx.Unlock()

	nextTime := s.kcp.Check()
	timerScheduler.PushTask(s.update, nextTime)
}

func NewSession(conn net.PacketConn, addr *net.UDPAddr, stateHandler ClientConnState) *Session {
	if conn == nil || addr == nil {
		panic("invalid params")
	}

	s := newSession(atomic.AddUint32(&convID, 1), nil, conn, addr, stateHandler)
	return s
}
