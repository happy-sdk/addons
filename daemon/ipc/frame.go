// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/happy-sdk/addons/daemon/ipc/ipcpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Frame struct {
	raw       *ipcpb.Frame
	metadatab []byte    // encrypted metadata bytes of the frame
	recvts    time.Time // time when the frame was received by reader
}

var validResponseKinds = []ipcpb.Frame_Kind{
	ipcpb.Frame_HANDSHAKE,
	ipcpb.Frame_RESPONSE,
	ipcpb.Frame_ERROR,
}

func (f *Frame) AsResponse() (*Response, error) {
	if !slices.Contains(validResponseKinds, f.raw.Metadata.Kind) {
		return nil, fmt.Errorf("%w: frame is not a response", Error)
	}

	var raw ipcpb.Response
	if err := proto.Unmarshal(f.raw.Payload, &raw); err != nil {
		return nil, fmt.Errorf("%w: unmarshaling frame response: %s", Error, err.Error())
	}

	res := &Response{
		raw:      &raw,
		metadata: f.raw.Metadata,
	}
	return res, nil
}

func (f *Frame) AsRequest() (*Request, error) {
	if f.raw.Metadata.Kind != ipcpb.Frame_HANDSHAKE && f.raw.Metadata.Kind != ipcpb.Frame_REQUEST {
		return nil, fmt.Errorf("%w: frame is not a request", Error)
	}
	var raw ipcpb.Request
	if err := proto.Unmarshal(f.raw.Payload, &raw); err != nil {
		return nil, fmt.Errorf("%w: unmarshaling frame request: %s", Error, err.Error())
	}

	req := &Request{
		id:       ipcpb.UUID{Value: uuid.New().String()},
		raw:      &raw,
		metadata: f.raw.Metadata,
		recvts:   f.recvts,
	}
	return req, nil
}

func (f *Frame) WriteTo(w io.Writer) (n int64, err error) {
	if err = binary.Write(w, binary.BigEndian, f.raw.MetadataLength); err != nil {
		return
	}
	n += 4

	var (
		n1 int
		n2 int
	)
	if n1, err = w.Write(f.metadatab); err != nil {
		return
	}
	n += int64(n1)
	if len(f.raw.Payload) > 0 {
		if n2, err = w.Write(f.raw.Payload); err != nil {
			return
		}
		n += int64(n2)
	}

	return
}

func NextFrame(conn net.Conn, key cipher.Block) (*Frame, error) {
	raw := &ipcpb.Frame{
		Metadata: &ipcpb.Frame_Metadata{},
	}

	if err := binary.Read(conn, binary.BigEndian, &raw.MetadataLength); err != nil {
		return nil, err
	}

	encMetadata := make([]byte, raw.MetadataLength)
	if _, err := io.ReadFull(conn, encMetadata); err != nil {
		return nil, err
	}

	now := time.Now()

	rawMetadata, err := Decrypt(key, encMetadata)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypting frame header: %s", Error, err.Error())
	}

	if err := proto.Unmarshal(rawMetadata, raw.Metadata); err != nil {
		return nil, fmt.Errorf("%w: unmarshaling frame header: %s", Error, err.Error())
	}

	if raw.Metadata != nil && raw.Metadata.Length > 0 {
		rawb := make([]byte, raw.Metadata.Length)
		if _, err := io.ReadFull(conn, rawb); err != nil {
			return nil, err
		}
		raw.Payload, err = Decrypt(key, rawb)
		if err != nil {
			return nil, fmt.Errorf("%w: decrypting frame: %s", Error, err.Error())
		}
	}

	return &Frame{
		raw:    raw,
		recvts: now,
	}, nil
}

type Request struct {
	id       ipcpb.UUID
	raw      *ipcpb.Request
	metadata *ipcpb.Frame_Metadata // structured metadata
	recvts   time.Time             // time when the request frame was received by reader
}

func NewRequest(raw *ipcpb.Request) *Request {
	return &Request{
		raw: raw,
	}
}

func (r *Request) frame(key cipher.Block) (*Frame, error) {
	frame := &Frame{
		raw: &ipcpb.Frame{
			Metadata: &ipcpb.Frame_Metadata{
				CorrelationId: &ipcpb.UUID{Value: r.id.String()},
			},
		},
	}

	switch r.raw.GetBody().(type) {
	case *ipcpb.Request_Handshake:
		frame.raw.Metadata.Kind = ipcpb.Frame_HANDSHAKE
	case *ipcpb.Request_FireAndForget,
		*ipcpb.Request_Command,
		*ipcpb.Request_Synchronous,
		*ipcpb.Request_Subscription:
		frame.raw.Metadata.Kind = ipcpb.Frame_REQUEST
	default:
		frame.raw.Metadata.Kind = ipcpb.Frame_INVALID
	}

	payload, err := proto.Marshal(r.raw)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame payload: %s", Error, err.Error())
	}

	// encrypt the payload
	encPayload, err := Encrypt(key, payload)
	if err != nil {
		return nil, fmt.Errorf("%w: frame payload encryption failed: %s", Error, err.Error())
	}
	frame.raw.Metadata.Length = uint32(len(encPayload))
	frame.raw.Payload = encPayload

	metadata, err := proto.Marshal(frame.raw.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame metadata: %s", Error, err.Error())
	}

	reqMetadata, err := Encrypt(key, metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: frame metadata encryption failed: %s", Error, err.Error())
	}
	frame.raw.MetadataLength = uint32(len(reqMetadata))
	frame.metadatab = reqMetadata

	return frame, nil
}

type Response struct {
	req      *Request
	raw      *ipcpb.Response
	metadata *ipcpb.Frame_Metadata
}

func NewResponse(raw *ipcpb.Response, req *Request) *Response {
	if raw.RequestId == nil && req != nil {
		raw.RequestId = &ipcpb.UUID{Value: req.id.String()}
	}

	if raw.Status == nil {
		raw.Status = &ipcpb.Response_Status{
			Type: ipcpb.Response_Status_SUCCESS,
		}
	}

	if raw.Status.Code == 0 {
		raw.Status.Code = uint32(raw.Status.Type)
	}

	return &Response{
		req:      req,
		raw:      raw,
		metadata: &ipcpb.Frame_Metadata{},
	}
}

func (r *Response) Metadata() *ipcpb.Frame_Metadata {
	return r.metadata
}

func (r *Response) RAW() *ipcpb.Response {
	return r.raw
}

func (r *Response) frame(key cipher.Block) (*Frame, error) {

	if r.metadata.Timestamp == nil {
		r.metadata.Timestamp = timestamppb.Now()
	}

	if r.raw.Timestamp == nil {
		r.raw.Timestamp = r.metadata.Timestamp
	}

	frame := &Frame{
		raw: &ipcpb.Frame{
			Metadata: r.metadata,
		},
	}

	switch r.raw.GetBody().(type) {
	case *ipcpb.Response_Handshake:
		frame.raw.Metadata.Kind = ipcpb.Frame_HANDSHAKE
	case *ipcpb.Response_Command,
		*ipcpb.Response_Synchronous,
		*ipcpb.Response_Subscription,
		*ipcpb.Response_Health:
		frame.raw.Metadata.Kind = ipcpb.Frame_RESPONSE
	default:
		frame.raw.Metadata.Kind = ipcpb.Frame_INVALID
	}

	payload, err := proto.Marshal(r.raw)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame payload: %s", Error, err.Error())
	}

	// encrypt the payload
	encPayload, err := Encrypt(key, payload)
	if err != nil {
		return nil, fmt.Errorf("%w: frame payload encryption failed: %s", Error, err.Error())
	}
	frame.raw.Metadata.Length = uint32(len(encPayload))
	if r.req != nil {
		frame.raw.Metadata.ReqDur = durationpb.New(time.Since(r.req.recvts))
	}
	frame.raw.Payload = encPayload

	metadata, err := proto.Marshal(frame.raw.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame metadata: %s", Error, err.Error())
	}

	reqMetadata, err := Encrypt(key, metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: frame metadata encryption failed: %s", Error, err.Error())
	}
	frame.raw.MetadataLength = uint32(len(reqMetadata))
	frame.metadatab = reqMetadata

	return frame, nil
}

type Ping struct {
	f *Frame
}

func NewPing(prevPongSeq uint64) (*Ping, error) {
	frame := &Frame{
		raw: &ipcpb.Frame{
			Metadata: &ipcpb.Frame_Metadata{
				Kind:      ipcpb.Frame_PING,
				Length:    0,
				Timestamp: timestamppb.New(time.Now()),
				Sequence:  &prevPongSeq,
			},
		},
	}

	return &Ping{
		f: frame,
	}, nil
}

func (p *Ping) frame(key cipher.Block) (*Frame, error) {

	metadata, err := proto.Marshal(p.f.raw.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame metadata: %s", Error, err.Error())
	}

	metadatab, err := Encrypt(key, metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: frame metadata encryption failed: %s", Error, err.Error())
	}
	p.f.raw.MetadataLength = uint32(len(metadatab))
	p.f.metadatab = metadatab

	return p.f, nil
}

type Pong struct {
	f    *Frame
	addr string
}

func NewPong(prevPongSeq uint64) (*Pong, error) {
	prevPongSeq++
	frame := &Frame{
		raw: &ipcpb.Frame{
			Metadata: &ipcpb.Frame_Metadata{
				Kind:      ipcpb.Frame_PONG,
				Length:    0,
				Timestamp: timestamppb.New(time.Now()),
				Sequence:  &prevPongSeq,
			},
		},
	}

	return &Pong{
		f: frame,
	}, nil
}

func (p *Pong) Duration() time.Duration {
	return p.f.raw.Metadata.ReqDur.AsDuration()
}

func (p *Pong) Len() int {
	return int(p.f.raw.MetadataLength)
}

func (p *Pong) Addr() string {
	return p.addr
}

func (p *Pong) Seq() uint64 {
	return p.f.raw.Metadata.GetSequence()
}

func (p *Pong) frame(key cipher.Block, reqts time.Time) (*Frame, error) {

	p.f.raw.Metadata.ReqDur = durationpb.New(time.Since(reqts))
	metadata, err := proto.Marshal(p.f.raw.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame metadata: %s", Error, err.Error())
	}

	metadatab, err := Encrypt(key, metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: frame metadata encryption failed: %s", Error, err.Error())
	}
	p.f.raw.MetadataLength = uint32(len(metadatab))
	p.f.metadatab = metadatab

	return p.f, nil
}

type ErrorFrame struct {
	f   *Frame
	res *ipcpb.Response
}

func NewRequestError(
	req *Request,
	status ipcpb.Response_Status_StatusType,
	msg string,
	details *ipcpb.Response_Details,
) *ErrorFrame {
	var (
		reqts         time.Time
		correlationId *ipcpb.UUID
	)

	if req != nil {
		reqts = req.recvts
		correlationId = req.metadata.CorrelationId
	}

	frame := &Frame{
		raw: &ipcpb.Frame{
			Metadata: &ipcpb.Frame_Metadata{
				Kind:          ipcpb.Frame_ERROR,
				Length:        0,
				Timestamp:     timestamppb.New(time.Now()),
				ReqDur:        durationpb.New(time.Since(reqts)),
				CorrelationId: correlationId,
			},
		},
	}

	return &ErrorFrame{
		f: frame,
		res: &ipcpb.Response{
			RequestId: &ipcpb.UUID{Value: req.id.String()},
			Type:      ipcpb.Response_ERROR,
			Timestamp: timestamppb.New(time.Now()),
			Status: &ipcpb.Response_Status{
				Type:    status,
				Code:    uint32(status),
				Message: &msg,
				Details: details,
			},
		},
	}
}

func (e *ErrorFrame) frame(key cipher.Block) (*Frame, error) {

	payload, err := proto.Marshal(e.res)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame payload: %s", Error, err.Error())
	}

	// encrypt the payload
	encPayload, err := Encrypt(key, payload)
	if err != nil {
		return nil, fmt.Errorf("%w: frame payload encryption failed: %s", Error, err.Error())
	}
	e.f.raw.Metadata.Length = uint32(len(encPayload))
	e.f.raw.Payload = encPayload

	metadata, err := proto.Marshal(e.f.raw.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal frame metadata: %s", Error, err.Error())
	}

	metadatab, err := Encrypt(key, metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: frame metadata encryption failed: %s", Error, err.Error())
	}
	e.f.raw.MetadataLength = uint32(len(metadatab))
	e.f.metadatab = metadatab

	return e.f, nil
}
