package protocol

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/vmihailenco/msgpack/v5"
)

// MaxMessageSize is the maximum allowed payload size (4 MB).
const MaxMessageSize = 4 * 1024 * 1024

// WriteMsg writes a length-prefixed msgpack envelope to w.
func WriteMsg(w io.Writer, env *Envelope) error {
	data, err := msgpack.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if len(data) > MaxMessageSize {
		return fmt.Errorf("message too large: %d > %d", len(data), MaxMessageSize)
	}

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// ReadMsg reads a length-prefixed msgpack envelope from r.
func ReadMsg(r io.Reader) (*Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}

	size := binary.BigEndian.Uint32(hdr[:])
	if size > MaxMessageSize {
		return nil, fmt.Errorf("message too large: %d > %d", size, MaxMessageSize)
	}

	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}

	var env Envelope
	if err := msgpack.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return &env, nil
}

// EncodeBody marshals v into a msgpack.RawMessage suitable for Envelope.Body.
func EncodeBody(v any) (msgpack.RawMessage, error) {
	return msgpack.Marshal(v)
}

// DecodeBody unmarshals an Envelope.Body into v.
func DecodeBody(body msgpack.RawMessage, v any) error {
	return msgpack.Unmarshal(body, v)
}

// NewEnvelope creates an Envelope with the given type, ID, and body.
func NewEnvelope(typ MsgType, id uint32, body any) (*Envelope, error) {
	raw, err := EncodeBody(body)
	if err != nil {
		return nil, err
	}
	return &Envelope{Type: typ, ID: id, Body: raw}, nil
}

// NewEnvelopeNoBody creates an Envelope with no body (nil Body).
func NewEnvelopeNoBody(typ MsgType, id uint32) *Envelope {
	return &Envelope{Type: typ, ID: id}
}
