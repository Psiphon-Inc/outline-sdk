// Copyright 2023 Jigsaw Operations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ssdial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/Jigsaw-Code/outline-sdk/transport"
	"github.com/shadowsocks/go-shadowsocks2/socks"
)

// TODO: DialWithConnOnWrite or DialOnWrite?
// Should be doable by the user.
// Implement general DialOnWrite given DialWithData.
// Perhaps StreamConn should have Writev([][]bytes). Writev can be implemented using ReadFrom(Buffers).
// Can have AsWriter[RF io.ReaderFrom](rf RF) Writer[RF]

// NewStreamDialer creates a client that routes connections to a Shadowsocks proxy listening at
// the given StreamEndpoint.
func NewStreamDialer(endpoint transport.StreamEndpoint) (*StreamDialer, error) {
	if endpoint == nil {
		return nil, errors.New("argument endpoint must not be nil")
	}
	d := StreamDialer{endpoint: endpoint, ClientDataWait: 10 * time.Millisecond}
	return &d, nil
}

type StreamDialer struct {
	endpoint transport.StreamEndpoint

	// ClientDataWait specifies the amount of time to wait for client data before sending
	// the Shadowsocks connection request to the proxy server. This value is 10 milliseconds
	// by default.
	//
	// StreamDialer has an optimization to send the initial client payload along with
	// the Shadowsocks connection request.  This saves one packet during connection, and also
	// reduces the distinctiveness of the connection pattern.
	//
	// Normally, the initial payload will be sent as soon as the socket is connected,
	// except for delays due to inter-process communication.  However, some protocols
	// expect the server to send data first, in which case there is no client payload.
	// We therefore use a short delay by default (10ms), longer than any reasonable IPC but shorter than
	// typical network latency.  (In an Android emulator, the 90th percentile delay
	// was ~1 ms.)  If no client payload is received by this time, we connect without it.
	ClientDataWait time.Duration
}

var _ transport.StreamDialer = (*StreamDialer)(nil)

// Dial implements StreamDialer.Dial using a Shadowsocks server.
//
// The Shadowsocks StreamDialer returns a connection after the connection to the proxy is established,
// but before the connection to the target is established. That means we cannot signal "connection refused"
// or "connection timeout" errors from the target to the application.
//
// This behavior breaks IPv6 Happy Eyeballs because the application IPv6 socket will connect successfully,
// even if the proxy fails to connect to the IPv6 destination. The broken Happy Eyeballs behavior makes
// IPv6 unusable if the proxy cannot use IPv6.
//
// We can't easily fix that issue because Shadowsocks, unlike SOCKS, does not have a way to indicate
// whether the target connection is successful. Even if that was possible, we want to wait until we have
// initial data from the application in order to send the Shadowsocks salt, SOCKS address and initial data
// all in one packet. This makes the size of the initial packet hard to predict, avoiding packet size
// fingerprinting. We can only get the application initial data if we return a connection first.
func (c *StreamDialer) Dial(ctx context.Context, remoteAddr string) (transport.StreamConn, error) {
	socksTargetAddr := socks.ParseAddr(remoteAddr)
	if socksTargetAddr == nil {
		return nil, errors.New("failed to parse target address")
	}
	proxyConn, err := c.endpoint.Connect(ctx)
	if err != nil {
		return nil, err
	}
	err = DialStreamConn(ctx, proxyConn, remoteAddr, nil)
	if err != nil {
		proxyConn.Close()
		return nil, err
	}
	return proxyConn, nil
}

// TODO: Introduce a generic wrapConn that inserts a prefix in the first Write. Need to account for ReadFrom.
func DialStreamConn(ctx context.Context, proxyConn transport.StreamConn, remoteAddr string, initialData []byte) error {
	socksTargetAddr := socks.ParseAddr(remoteAddr)
	if socksTargetAddr == nil {
		return errors.New("failed to parse target address")
	}
	var err error
	if len(initialData) == 0 {
		_, err = proxyConn.Write(socksTargetAddr)
	} else if rf, ok := proxyConn.(io.ReaderFrom); ok {
		buffers := net.Buffers([][]byte{socksTargetAddr, initialData})
		_, err = rf.ReadFrom(&buffers)
	} else {
		firstWrite := append(socksTargetAddr, initialData...)
		_, err = proxyConn.Write(firstWrite)
	}
	if err != nil {
		proxyConn.Close()
		return fmt.Errorf("failed to write: %w", err)
	}
	return nil
}
