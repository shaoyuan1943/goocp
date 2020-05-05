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

type PacketCrypto interface {
	SetKey(key []byte)
	SetLocalNonce(nonce []byte) error
	GetLocalNonce() []byte
	Encrypto(dst, src []byte) error
	Decrypto(dst, src []byte) error
}
