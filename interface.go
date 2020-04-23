package goocp

type ServerConnState interface {
	OnNewSessionComing(newSession *Session)
	OnServerClosed(err error, reason string)
}

type ClientConnState interface {
	OnClosed(err error)
	OnSendDataError(data []byte, err error)
	OnNewDataComing(data []byte)
}