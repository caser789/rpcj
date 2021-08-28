package share

import (
	"github.com/caser789/rpcj/codec"
	"github.com/caser789/rpcj/protocol"
)

const (
	// DefaultRPCPath is used by ServeHTTP.
	DefaultRPCPath = "/_rpcx_"

	// AuthKey is used in metadata.
	AuthKey = "__AUTH"

	// OpentracingSpanServerKey key in service context
	OpentracingSpanServerKey = "opentracing_span_server_key"
	// OpentracingSpanClientKey key in client context
	OpentracingSpanClientKey = "opentracing_span_client_key"
)

var (
	// Codecs are codecs supported by rpcx.
	Codecs = map[protocol.SerializeType]codec.Codec{
		protocol.SerializeNone: &codec.ByteCodec{},
		protocol.JSON:          &codec.JSONCodec{},
		protocol.ProtoBuffer:   &codec.PBCodec{},
		protocol.MsgPack:       &codec.MsgpackCodec{},
		protocol.Thrift:        &codec.ThriftCodec{},
	}
)

// RegisterCodec register customized codec.
func RegisterCodec(t protocol.SerializeType, c codec.Codec) {
	Codecs[t] = c
}

// ContextKey defines key type in context.
type ContextKey string

// ReqMetaDataKey is used to set metatdata in context of requests.
var ReqMetaDataKey = ContextKey("__req_metadata")

// ResMetaDataKey is used to set metatdata in context of responses.
var ResMetaDataKey = ContextKey("__res_metadata")
