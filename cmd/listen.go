// Copyright © 2020 Karim Radhouani <medkarimrdi@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/karimra/gnmic/outputs"
	nokiasros "github.com/karimra/sros-dialout"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func newListenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "listen",
		Short:        "listens for telemetry dialout updates from the node",
		RunE:         runListenCmd,
		SilenceUsage: true,
	}
	cmd.Flags().Uint32P("max-concurrent-streams", "", 256, "max concurrent streams gnmic can receive per transport")
	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		cli.config.BindPFlag(cmd.Name()+"-"+flag.Name, flag)
	})
	return cmd
}

type dialoutTelemetryServer struct {
	listener   net.Listener
	grpcServer *grpc.Server
	Outputs    map[string]outputs.Output

	ctx context.Context
}

func (s *dialoutTelemetryServer) Publish(stream nokiasros.DialoutTelemetry_PublishServer) error {
	peer, ok := peer.FromContext(stream.Context())
	if ok && cli.config.Debug {
		b, err := json.Marshal(peer)
		if err != nil {
			cli.logger.Printf("failed to marshal peer data: %v", err)
		} else {
			cli.logger.Printf("received Publish RPC from peer=%s", string(b))
		}
	}
	md, ok := metadata.FromIncomingContext(stream.Context())
	if ok && cli.config.Debug {
		b, err := json.Marshal(md)
		if err != nil {
			cli.logger.Printf("failed to marshal context metadata: %v", err)
		} else {
			cli.logger.Printf("received http2_header=%s", string(b))
		}
	}
	outMeta := outputs.Meta{}
	meta := make(map[string]interface{})
	if sn, ok := md["subscription-name"]; ok {
		if len(sn) > 0 {
			meta["subscription-name"] = sn[0]
			outMeta["subscription-name"] = sn[0]
		}
	} else {
		cli.logger.Println("could not find subscription-name in http2 headers")
	}
	meta["source"] = peer.Addr.String()
	outMeta["source"] = peer.Addr.String()
	if systemName, ok := md["system-name"]; ok {
		if len(systemName) > 0 {
			meta["system-name"] = systemName[0]
		}
	} else {
		cli.logger.Println("could not find system-name in http2 headers")
	}
	//lock := new(sync.Mutex)
	for {
		subResp, err := stream.Recv()
		if err != nil {
			if err != io.EOF {
				cli.logger.Printf("gRPC dialout receive error: %v", err)
			}
			break
		}
		err = stream.Send(&nokiasros.PublishResponse{})
		if err != nil {
			cli.logger.Printf("error sending publish response to server: %v", err)
		}
		switch resp := subResp.Response.(type) {
		case *gnmi.SubscribeResponse_Update:
			// b, err := formatSubscribeResponse(meta, subResp)
			// if err != nil {
			// 	logger.Printf("failed to format subscribe response: %v", err)
			// 	continue
			// }
			for _, o := range s.Outputs {
				go o.Write(s.ctx, subResp, outMeta)
			}
			// buff := new(bytes.Buffer)
			// err = json.Indent(buff, b, "", "  ")
			// if err != nil {
			// 	logger.Printf("failed to indent msg: err=%v, msg=%s", err, string(b))
			// 	continue
			// }
			// lock.Lock()
			// fmt.Println(buff.String())
			// lock.Unlock()
		case *gnmi.SubscribeResponse_SyncResponse:
			cli.logger.Printf("received sync response=%+v from %s\n", resp.SyncResponse, meta["source"])
		}
	}
	return nil
}

func runListenCmd(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := new(dialoutTelemetryServer)
	server.ctx = ctx
	address := cli.config.Address
	if len(address) == 0 {
		return fmt.Errorf("no address specified")
	}
	if len(address) > 1 {
		fmt.Printf("multiple addresses specified, listening only on %s\n", address[0])
	}
	server.Outputs = make(map[string]outputs.Output)
	outCfgs, err := cli.config.GetOutputs()
	if err != nil {
		return err
	}
	for name, outConf := range outCfgs {
		if outType, ok := outConf["type"]; ok {
			if initializer, ok := outputs.Outputs[outType.(string)]; ok {
				out := initializer()
				go out.Init(ctx, outConf, outputs.WithLogger(cli.logger))
				server.Outputs[name] = out
			}
		}
	}

	defer func() {
		for _, o := range server.Outputs {
			o.Close()
		}
	}()
	server.listener, err = net.Listen("tcp", address[0])
	if err != nil {
		return err
	}
	cli.logger.Printf("waiting for connections on %s", address[0])
	var opts []grpc.ServerOption
	if cli.config.MaxMsgSize > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(int(cli.config.MaxMsgSize)))
	}
	opts = append(opts,
		grpc.MaxConcurrentStreams(cli.config.ListenMaxConcurrentStreams),
		grpc.StreamInterceptor(grpc_prometheus.StreamServerInterceptor))

	if cli.config.TLSKey != "" && cli.config.TLSCert != "" {
		tlsConfig := &tls.Config{
			Renegotiation:      tls.RenegotiateNever,
			InsecureSkipVerify: cli.config.SkipVerify,
		}
		err := loadCerts(tlsConfig)
		if err != nil {
			cli.logger.Printf("failed loading certificates: %v", err)
		}

		err = loadCACerts(tlsConfig)
		if err != nil {
			cli.logger.Printf("failed loading CA certificates: %v", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}

	server.grpcServer = grpc.NewServer(opts...)
	nokiasros.RegisterDialoutTelemetryServer(server.grpcServer, server)
	grpc_prometheus.Register(server.grpcServer)

	httpServer := &http.Server{
		Handler: promhttp.Handler(),
		Addr:    cli.config.PrometheusAddress,
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil {
			cli.logger.Printf("Unable to start prometheus http server.")
		}
	}()
	defer httpServer.Close()
	server.grpcServer.Serve(server.listener)
	defer server.grpcServer.Stop()
	return nil
}
