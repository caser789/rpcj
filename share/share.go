package share

import (
	"github.com/caser789/rpcj/codec"
	"github.com/caser789/rpcj/protocol"
)

const (
	// DefaultRPCPath is used by ServeHTTP
	DefaultRPCPath = "/_rpcx_"
)

var (
	// Codecs are codecs supported by rpcx.
	Codecs = map[protocol.SerializeType]codec.Codec{
		protocol.SerializeNone: &codec.ByteCodec{},
		protocol.JSON:          &codec.JSONCodec{},
		protocol.ProtoBuffer:   &codec.PBCodec{},
		protocol.MsgPack:       &codec.MsgpackCodec{},
	}
)
