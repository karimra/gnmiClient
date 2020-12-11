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
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/karimra/gnmic/collector"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var paths []string
var dataType = [][2]string{
	{"all", "all config/state/operational data"},
	{"config", "data that the target considers to be read/write"},
	{"state", "read-only data on the target"},
	{"operational", "read-only data on the target that is related to software processes operating on the device, or external interactions of the device"},
}

func newGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "run gnmi get on targets",
		Annotations: map[string]string{
			"--path":   "XPATH",
			"--prefix": "PREFIX",
			"--model":  "MODEL",
			"--type":   "STORE",
		},
		SilenceUsage: true,
		RunE:         runGetCmd,
		PostRun: func(cmd *cobra.Command, args []string) {
			cmd.ResetFlags()
			initGetFlags(cmd)
		},
	}
	initGetFlags(cmd)
	return cmd
}

func getRequest(ctx context.Context, tName string, req *gnmi.GetRequest, wg *sync.WaitGroup, lock *sync.Mutex) {
	defer wg.Done()
	xreq := req
	models := cli.config.GetModel
	if len(models) > 0 {
		spModels, unspModels, err := filterModels(ctx, cli.collector, tName, models)
		if err != nil {
			cli.logger.Printf("failed getting supported models from '%s': %v", tName, err)
			return
		}
		if len(unspModels) > 0 {
			cli.logger.Printf("found unsupported models for target '%s': %+v", tName, unspModels)
		}
		for _, m := range spModels {
			xreq.UseModels = append(xreq.UseModels, m)
		}
	}
	if cli.config.PrintRequest {
		lock.Lock()
		fmt.Fprint(os.Stderr, "Get Request:\n")
		err := printMsg(tName, req)
		if err != nil {
			cli.logger.Printf("error marshaling get request msg: %v", err)
			if !cli.config.Log {
				fmt.Printf("error marshaling get request msg: %v\n", err)
			}
		}
		lock.Unlock()
	}
	cli.logger.Printf("sending gNMI GetRequest: prefix='%v', path='%v', type='%v', encoding='%v', models='%+v', extension='%+v' to %s",
		xreq.Prefix, xreq.Path, xreq.Type, xreq.Encoding, xreq.UseModels, xreq.Extension, tName)
	response, err := cli.collector.Get(ctx, tName, xreq)
	if err != nil {
		cli.logger.Printf("failed sending GetRequest to %s: %v", tName, err)
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(os.Stderr, "Get Response:\n")
	err = printMsg(tName, response)
	if err != nil {
		cli.logger.Printf("error marshaling get response from %s: %v", tName, err)
		if !cli.config.Log {
			fmt.Printf("error marshaling get response from %s: %v\n", tName, err)
		}
	}
}

// used to init or reset getCmd flags for gnmic-prompt mode
func initGetFlags(cmd *cobra.Command) {
	cmd.Flags().StringArrayVarP(&paths, "path", "", []string{}, "get request paths")
	cmd.MarkFlagRequired("path")
	cmd.Flags().StringP("prefix", "", "", "get request prefix")
	cmd.Flags().StringSliceP("model", "", []string{}, "get request models")
	cmd.Flags().StringP("type", "t", "ALL", "data type requested from the target. one of: ALL, CONFIG, STATE, OPERATIONAL")
	cmd.Flags().StringP("target", "", "", "get request target")

	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		cli.config.BindPFlag(cmd.Name()+"-"+flag.Name, flag)
	})
}

func createGetRequest() (*gnmi.GetRequest, error) {
	encodingVal, ok := gnmi.Encoding_value[strings.Replace(strings.ToUpper(cli.config.Encoding), "-", "_", -1)]
	if !ok {
		return nil, fmt.Errorf("invalid encoding type '%s'", cli.config.Encoding)
	}
	req := &gnmi.GetRequest{
		UseModels: make([]*gnmi.ModelData, 0),
		Path:      make([]*gnmi.Path, 0, len(paths)),
		Encoding:  gnmi.Encoding(encodingVal),
	}
	prefix := cli.config.GetPrefix
	if prefix != "" {
		gnmiPrefix, err := collector.ParsePath(prefix)
		if err != nil {
			return nil, fmt.Errorf("prefix parse error: %v", err)
		}
		req.Prefix = gnmiPrefix
	}
	dataType := cli.config.GetType
	if dataType != "" {
		dti, ok := gnmi.GetRequest_DataType_value[strings.ToUpper(dataType)]
		if !ok {
			return nil, fmt.Errorf("unknown data type %s", dataType)
		}
		req.Type = gnmi.GetRequest_DataType(dti)
	}
	for _, p := range paths {
		gnmiPath, err := collector.ParsePath(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("path parse error: %v", err)
		}
		req.Path = append(req.Path, gnmiPath)
	}
	return req, nil
}

func runGetCmd(cmd *cobra.Command, args []string) error {
	if cli.config.Format == "event" {
		return fmt.Errorf("format event not supported for Get RPC")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setupCloseHandler(cancel)
	targetsConfig, err := cli.config.GetTargets()
	if err != nil {
		return fmt.Errorf("failed getting targets config: %v", err)
	}

	subscriptionsConfig, err := cli.config.GetSubscriptions()
	if err != nil {
		return fmt.Errorf("failed getting subscriptions config: %v", err)
	}

	outs, err := cli.config.GetOutputs()
	if err != nil {
		return err
	}

	if cli.collector == nil {
		cfg := &collector.Config{
			Debug:               cli.config.Debug,
			Format:              cli.config.Format,
			TargetReceiveBuffer: cli.config.TargetBufferSize,
			RetryTimer:          cli.config.Retry,
		}

		cli.collector = collector.NewCollector(cfg, targetsConfig,
			collector.WithDialOptions(createCollectorDialOpts()),
			collector.WithSubscriptions(subscriptionsConfig),
			collector.WithOutputs(outs),
			collector.WithLogger(cli.logger),
		)
	} else {
		// prompt mode
		for _, tc := range targetsConfig {
			cli.collector.AddTarget(tc)
		}
	}
	req, err := createGetRequest()
	if err != nil {
		return err
	}
	wg := new(sync.WaitGroup)
	wg.Add(len(targetsConfig))
	lock := new(sync.Mutex)
	for tName := range targetsConfig {
		go getRequest(ctx, tName, req, wg, lock)
	}
	wg.Wait()
	return nil
}
