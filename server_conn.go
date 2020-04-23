package goocp

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shaoyuan1943/gokcp"
)

type ServerConn struct {
	rwc          net.PacketConn
	closedC      chan struct{}
	allSessions  map[string]*Session
	state        ServerConnState
	started      int64
	closed       bool
	mx           sync.Mutex
	errSessionsC chan *Session
}

func (sc *ServerConn) SetConnState(state ServerConnState) {
	if state != nil {
		sc.state = state
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

	if sc.state != nil {
		sc.state.OnServerClosed(err, reason)
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

		session, ok := sc.allSessions[addr.String()]
		if !ok {
			session = newSessionFromServer(sc, sc.rwc, addr, true)
			sc.allSessions[addr.String()] = session
			if sc.state != nil {
				sc.state.OnNewSessionComing(session)
			}
		}

		err = session.onKCPDataComing(buffer[:n])
		if err != nil {
			errTimes++
		}
	}
}

func (sc *ServerConn) closeSession(session *Session) {
	if _, ok := sc.allSessions[session.remoteAddr.String()]; ok {
		delete(sc.allSessions, session.remoteAddr.String())
	}

	session.quitF()
	if session.state != nil {
		session.state.OnClosed(session.err)
	}
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

			sc.closeSession(session)
		case <-sc.closedC:
			return
		default:
		}
	}
}

func Listen(addr string, state ServerConnState) (*ServerConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}

	network := "udp4"
	if udpAddr.IP.To4() == nil {
		network = "udp6"
	}

	udpConn, err := net.ListenUDP(network, udpAddr)
	if err != nil {
		return nil, err
	}

	server := &ServerConn{rwc: udpConn}
	server.errSessionsC = make(chan *Session, 32)
	server.SetConnState(state)

	go server.readLoop()
	go server.checkSession()
	return server, nil
}
