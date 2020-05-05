package goocp

import (
	"encoding/binary"
	"hash/crc32"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/shaoyuan1943/gokcp"
)

var (
	HEADER_CMD_DATA       uint16 = 1
	HEADER_CMD_DISCONNECT uint16 = 2
	HEADER_SIZE           uint16 = 14
)

type ServerConn struct {
	rwc          net.PacketConn
	closedC      chan struct{}
	allSessions  map[string]*Session
	stateHandler ServerConnState
	started      int64
	closed       bool
	mx           sync.Mutex
	errSessionsC chan *Session
	codec        PacketCrypto
}

func (sc *ServerConn) SetServerConnStateHandler(stateHandler ServerConnState) {
	if stateHandler != nil {
		sc.stateHandler = stateHandler
	}
}

func (s *ServerConn) SetEncryptoCodec(codec PacketCrypto) {
	if codec != nil {
		s.codec = codec
	}
}

func (sc *ServerConn) Start() {
	atomic.AddInt64(&sc.started, 1)
}

func (sc *ServerConn) waitStart() {
	for {
		if atomic.LoadInt64(&sc.started) != 0 {
			return
		}

		select {
		case <-sc.closedC:
			return
		default:
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func (sc *ServerConn) Close(err error, reason string) {
	sc.close(err, reason)
}

func (sc *ServerConn) close(err error, reason string) {
	sc.mx.Lock()
	defer sc.mx.Unlock()

	if sc.closed {
		return
	}

	sc.closed = true
	sc.rwc.Close()
	close(sc.closedC)
	if sc.stateHandler != nil {
		sc.stateHandler.OnServerClosed(err, reason)
	}
}

func (sc *ServerConn) readLoop() {
	sc.waitStart()

	errTimes := 0
	buffer := make([]byte, gokcp.KCP_MTU_DEF+150)
	for {
		n, addr, err := sc.rwc.ReadFrom(buffer)
		if err != nil {
			sc.close(err, "ServerConn.readLoop")
			return
		}

		if n < int(HEADER_SIZE)+int(gokcp.KCP_OVERHEAD) {
			// error
			continue
		}

		err = sc.handleKCPData(addr, buffer[:n])
		if err != nil {
			errTimes++
		}
	}
}

/*
	packet format:
	|--NONCE--|--SUM--|--CMD--|--KCP-HEADER--|--USER-DATA--|
	|----8----|---4---|---2---|------24------|-------------|
	HEADER_SIZE: 8 + 4 + 2
*/
func (sc *ServerConn) handleKCPData(addr net.Addr, data []byte) error {
	if sc.codec != nil {
		// 1. encrypto
		err := sc.codec.Decrypto(data, data)
		if err != nil {
			return err
		}
	}

	// 2. check sum
	sum1 := crc32.ChecksumIEEE(data[14:])
	sum2 := binary.LittleEndian.Uint32(data[8:])
	if sum1 != sum2 {
		return errors.New("different data sum")
	}
	data = data[12:]

	// 3. check data cmd
	cmd := binary.LittleEndian.Uint16(data)
	if cmd == HEADER_CMD_DISCONNECT {
		session, ok := sc.allSessions[addr.String()]
		if !ok {
			return nil
		}

		session.close(errors.New("remote session closed"), false)
		return nil
	} else if cmd == HEADER_CMD_DATA {
		data = data[2:]
		convID := binary.LittleEndian.Uint32(data)
		sn := binary.LittleEndian.Uint32(data[12:])
		if convID == 0 {
			return errors.New("invalid kcp data")
		}

		sc.mx.Lock()
		session, ok := sc.allSessions[addr.String()]
		sc.mx.Unlock()

		valid := false
		if ok {
			if convID == session.kcp.ConvID() {
				valid = true
			} else {
				if sn == 0 {
					session.close(nil, false)
					session = nil
					session = newSessionFromServer(convID, sc, sc.rwc, addr)
					if sc.stateHandler != nil {
						sc.stateHandler.OnNewSessionComing(session)
					}
					valid = true
				}
			}
		} else {
			session = newSessionFromServer(convID, sc, sc.rwc, addr)
			sc.allSessions[addr.String()] = session
			if sc.stateHandler != nil {
				sc.stateHandler.OnNewSessionComing(session)
			}
			valid = true
		}

		if valid {
			session.onKCPDataComing(data)
		} else {
			return errors.New("different kcp data")
		}
	} else {
		return errors.New("unknow data cmd")
	}

	return nil
}

func (sc *ServerConn) notifyErrSession(session *Session) {
	sc.errSessionsC <- session
}

func (sc *ServerConn) checkSession() {
	for {
		select {
		case session := <-sc.errSessionsC:
			if _, ok := sc.allSessions[session.remoteAddr.String()]; ok {
				delete(sc.allSessions, session.remoteAddr.String())
			}
		case <-sc.closedC:
			return
		}
	}
}

func NewServerConn(conn net.PacketConn, stateHandler ServerConnState) *ServerConn {
	if conn == nil {
		panic("invlid params")
	}

	sc := &ServerConn{rwc: conn}
	sc.allSessions = make(map[string]*Session)
	sc.errSessionsC = make(chan *Session, 32)
	sc.closedC = make(chan struct{})
	sc.SetServerConnStateHandler(stateHandler)

	go sc.readLoop()
	go sc.checkSession()
	return sc
}
