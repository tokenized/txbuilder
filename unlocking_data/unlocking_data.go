package unlocking_data

import (
	"bytes"
	"fmt"

	"github.com/tokenized/channels"
	envelope "github.com/tokenized/envelope/pkg/golang/envelope/base"
	"github.com/tokenized/pkg/bitcoin"

	"github.com/pkg/errors"
)

var (
	ProtocolID = envelope.ProtocolID("UL") // Protocol ID for unlocking data messages
	Version    = uint8(0)

	ErrUnsupportedVersion = errors.New("Unsupported Operator Version")
)

type Protocol struct{}

func NewProtocol() *Protocol {
	return &Protocol{}
}

func (*Protocol) ProtocolID() envelope.ProtocolID {
	return ProtocolID
}

func (*Protocol) Parse(payload envelope.Data) (channels.Message, envelope.Data, error) {
	return Parse(payload)
}

func (*Protocol) ResponseCodeToString(code uint32) string {
	return "parse_error"
}

type UnlockingData struct {
	Size  uint64
	Value uint64
}

func (*UnlockingData) ProtocolID() envelope.ProtocolID {
	return ProtocolID
}

func (m *UnlockingData) Write() (envelope.Data, error) {
	// Version
	payload := bitcoin.ScriptItems{bitcoin.PushNumberScriptItem(int64(Version))}

	// Message
	payload = append(payload, bitcoin.PushNumberScriptItemUnsigned(m.Size))
	payload = append(payload, bitcoin.PushNumberScriptItemUnsigned(m.Value))

	return envelope.Data{envelope.ProtocolIDs{ProtocolID}, payload}, nil
}

func Parse(payload envelope.Data) (channels.Message, envelope.Data, error) {
	if len(payload.ProtocolIDs) == 0 {
		return nil, payload, nil
	}

	if !bytes.Equal(payload.ProtocolIDs[0], ProtocolID) {
		return nil, payload, nil
	}
	payload.ProtocolIDs = payload.ProtocolIDs[1:]

	if len(payload.Payload) < 3 {
		return nil, payload, errors.Wrapf(channels.ErrInvalidMessage, "3 push datas needed")
	}

	version, err := bitcoin.ScriptNumberValue(payload.Payload[0])
	if err != nil {
		return nil, payload, errors.Wrap(err, "version")
	}
	if version != 0 {
		return nil, payload, errors.Wrap(ErrUnsupportedVersion, fmt.Sprintf("%d", version))
	}

	size, err := bitcoin.ScriptNumberValueUnsigned(payload.Payload[1])
	if err != nil {
		return nil, payload, errors.Wrap(err, "size script number")
	}

	value, err := bitcoin.ScriptNumberValueUnsigned(payload.Payload[2])
	if err != nil {
		return nil, payload, errors.Wrap(err, "value script number")
	}

	payload.Payload = payload.Payload[3:]

	result := &UnlockingData{
		Size:  size,
		Value: value,
	}

	return result, payload, nil
}
