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
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/karimra/gnmic/collector"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var paths []string
var dataType = [][2]string{
	{"all", "all config/state/operational data"},
	{"config", "data that the target considers to be read/write"},
	{"state", "read-only data on the target"},
	{"operational", "read-only data on the target that is related to software processes operating on the device, or external interactions of the device"},
}

// getCmd represents the get command
var getCmd = &cobra.Command{
	Use:   "get",
	Short: "run gnmi get on targets",
	Annotations: map[string]string{
		"--path":   "XPATH",
		"--prefix": "PREFIX",
		"--model":  "MODEL",
		"--type":   "STORE",
	},

	RunE: func(cmd *cobra.Command, args []string) error {
		if viper.GetString("format") == "event" {
			return fmt.Errorf("format event not supported for Get RPC")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		setupCloseHandler(cancel)
		targetsConfig, err := createTargets()
		if err != nil {
			return err
		}
		if coll == nil {
			cfg := &collector.Config{
				Debug:      viper.GetBool("debug"),
				Format:     viper.GetString("format"),
				RetryTimer: viper.GetDuration("retry-timer"),
			}
			coll = collector.NewCollector(cfg, targetsConfig, collector.WithLogger(logger), collector.WithDialOptions(createCollectorDialOpts()))
		}

		req, err := createGetRequest()
		if err != nil {
			return err
		}
		wg := new(sync.WaitGroup)
		wg.Add(len(coll.Targets))
		lock := new(sync.Mutex)
		for tName := range coll.Targets {
			go getRequest(ctx, req, tName, wg, lock)
		}
		wg.Wait()
		return nil
	},
	PostRun: func(cmd *cobra.Command, args []string) {
		cmd.ResetFlags()
		initGetFlags(cmd)
	},
	SilenceUsage: true,
}

func getRequest(ctx context.Context, req *gnmi.GetRequest, tName string, wg *sync.WaitGroup, lock *sync.Mutex) {
	defer wg.Done()
	xreq := req
	models := viper.GetStringSlice("get-model")
	if len(models) > 0 {
		spModels, unspModels, err := filterModels(ctx, tName, models)
		if err != nil {
			logger.Printf("failed getting supported models from '%s': %v", tName, err)
			return
		}
		if len(unspModels) > 0 {
			logger.Printf("found unsupported models for target '%s': %+v", tName, unspModels)
		}
		for _, m := range spModels {
			xreq.UseModels = append(xreq.UseModels, m)
		}
	}
	if viper.GetBool("print-request") {
		lock.Lock()
		fmt.Fprint(os.Stderr, "Get Request:\n")
		err := printMsg(tName, req)
		if err != nil {
			logger.Printf("error marshaling get request msg: %v", err)
			if !viper.GetBool("log") {
				fmt.Printf("error marshaling get request msg: %v\n", err)
			}
		}
		lock.Unlock()
	}
	logger.Printf("sending gNMI GetRequest: prefix='%v', path='%v', type='%v', encoding='%v', models='%+v', extension='%+v' to %s",
		xreq.Prefix, xreq.Path, xreq.Type, xreq.Encoding, xreq.UseModels, xreq.Extension, tName)
	response, err := coll.Get(ctx, tName, xreq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Printf("failed to create a gRPC client for target '%s', timeout reached", tName)
			if !viper.GetBool("log") {
				fmt.Printf("failed to create a gRPC client for target '%s', timeout reached\n", tName)
			}
			return
		}
		logger.Printf("failed to create a gRPC client for target '%s' : %v", tName, err)
		if !viper.GetBool("log") {
			fmt.Printf("failed to create a gRPC client for target '%s' : %v\n", tName, err)
		}
		return
	}
	lock.Lock()
	defer lock.Unlock()
	fmt.Fprint(os.Stderr, "Get Response:\n")
	err = printMsg(tName, response)
	if err != nil {
		logger.Printf("error marshaling get response from %s: %v", tName, err)
		if !viper.GetBool("log") {
			fmt.Printf("error marshaling get response from %s: %v\n", tName, err)
		}
	}
}

func init() {
	rootCmd.AddCommand(getCmd)
	initGetFlags(getCmd)
}

// used to init or reset getCmd flags for gnmic-prompt mode
func initGetFlags(cmd *cobra.Command) {
	cmd.Flags().StringArrayVarP(&paths, "path", "", []string{}, "get request paths")
	cmd.MarkFlagRequired("path")
	cmd.Flags().StringP("prefix", "", "", "get request prefix")
	cmd.Flags().StringSliceP("model", "", []string{}, "get request models")
	cmd.Flags().StringP("type", "t", "ALL", "data type requested from the target. one of: ALL, CONFIG, STATE, OPERATIONAL")
	cmd.Flags().StringP("target", "", "", "get request target")

	viper.BindPFlag("get-path", cmd.LocalFlags().Lookup("path"))
	viper.BindPFlag("get-prefix", cmd.LocalFlags().Lookup("prefix"))
	viper.BindPFlag("get-model", cmd.LocalFlags().Lookup("model"))
	viper.BindPFlag("get-type", cmd.LocalFlags().Lookup("type"))
	viper.BindPFlag("get-target", cmd.LocalFlags().Lookup("target"))
}

func createGetRequest() (*gnmi.GetRequest, error) {
	encodingVal, ok := gnmi.Encoding_value[strings.Replace(strings.ToUpper(viper.GetString("encoding")), "-", "_", -1)]
	if !ok {
		return nil, fmt.Errorf("invalid encoding type '%s'", viper.GetString("encoding"))
	}
	req := &gnmi.GetRequest{
		UseModels: make([]*gnmi.ModelData, 0),
		Path:      make([]*gnmi.Path, 0, len(paths)),
		Encoding:  gnmi.Encoding(encodingVal),
	}
	prefix := viper.GetString("get-prefix")
	if prefix != "" {
		gnmiPrefix, err := collector.ParsePath(prefix)
		if err != nil {
			return nil, fmt.Errorf("prefix parse error: %v", err)
		}
		req.Prefix = gnmiPrefix
	}
	dataType := viper.GetString("get-type")
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
