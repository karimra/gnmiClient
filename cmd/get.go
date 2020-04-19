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
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/google/gnxi/utils/xpath"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc/metadata"
)

// getCmd represents the get command
var getCmd = &cobra.Command{
	Use:   "get",
	Short: "run gnmi get on targets",

	RunE: func(cmd *cobra.Command, args []string) error {
		debug := viper.GetBool("debug")
		var err error
		addresses := viper.GetStringSlice("address")
		addresses, err = selectTargets(addresses)
		if err != nil {
			return err
		}
		if len(viper.GetStringSlice("get-path")) == 0 && viper.GetString("yang-file") != "" {
			result, err := selectPaths()
			if err != nil {
				return err
			}
			viper.Set("get-path", result)
		} 
		username := viper.GetString("username")
		if username == "" {
			if username, err = readUsername(); err != nil {
				return err
			}
		}
		password := viper.GetString("password")
		if password == "" {
			if password, err = readPassword(); err != nil {
				return err
			}
		}

		encodingVal, ok := gnmi.Encoding_value[strings.Replace(strings.ToUpper(viper.GetString("encoding")), "-", "_", -1)]
		if !ok {
			return fmt.Errorf("invalid encoding type '%s'", viper.GetString("encoding"))
		}
		req := &gnmi.GetRequest{
			UseModels: make([]*gnmi.ModelData, 0),
			Path:      make([]*gnmi.Path, 0),
			Encoding:  gnmi.Encoding(encodingVal),
		}
		model := viper.GetString("get-model")
		prefix := viper.GetString("get-prefix")
		if prefix != "" {
			gnmiPrefix, err := xpath.ToGNMIPath(prefix)
			if err != nil {
				return fmt.Errorf("prefix parse error: %v", err)
			}
			req.Prefix = gnmiPrefix
		}
		paths := viper.GetStringSlice("get-path")
		for _, p := range paths {
			gnmiPath, err := xpath.ToGNMIPath(p)
			if err != nil {
				return fmt.Errorf("path parse error: %v", err)
			}
			req.Path = append(req.Path, gnmiPath)
		}
		dataType := viper.GetString("get-type")
		if dataType != "" {
			dti, ok := gnmi.GetRequest_DataType_value[strings.ToUpper(dataType)]
			if !ok {
				return fmt.Errorf("unknown data type %s", dataType)
			}
			req.Type = gnmi.GetRequest_DataType(dti)
		}
		if debug {
			log.Printf("DEBUG: request: %v", req)
		}
		wg := new(sync.WaitGroup)
		wg.Add(len(addresses))
		lock := new(sync.Mutex)
		for _, addr := range addresses {
			go func(address string) {
				defer wg.Done()
				ipa, _, err := net.SplitHostPort(address)
				if err != nil {
					if strings.Contains(err.Error(), "missing port in address") {
						address = net.JoinHostPort(ipa, defaultGrpcPort)
					} else {
						log.Printf("error parsing address '%s': %v", address, err)
						return
					}
				}
				conn, err := createGrpcConn(address)
				if err != nil {
					log.Printf("connection to %s failed: %v", address, err)
					return
				}
				client := gnmi.NewGNMIClient(conn)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx = metadata.AppendToOutgoingContext(ctx, "username", username, "password", password)
				xreq := req
				if model != "" {
					capResp, err := client.Capabilities(ctx, &gnmi.CapabilityRequest{})
					if err != nil {
						log.Printf("%v", err)
						return
					}
					var found bool
					for _, m := range capResp.SupportedModels {
						if m.Name == model {
							if debug {
								log.Printf("target %s: found model: %v\n", address, m)
							}
							xreq.UseModels = append(xreq.UseModels,
								&gnmi.ModelData{
									Name:         model,
									Organization: m.Organization,
									Version:      m.Version,
								})
							found = true
							break
						}
					}
					if !found {
						log.Printf("model '%s' not supported by target %s", model, address)
						return
					}
				}
				response, err := client.Get(ctx, xreq)
				if err != nil {
					log.Printf("error sending get request: %v", err)
					return
				}
				printPrefix := ""
				if len(addresses) > 1 && !viper.GetBool("no-prefix") {
					printPrefix = fmt.Sprintf("[%s] ", address)
				}
				lock.Lock()
				for _, notif := range response.Notification {
					fmt.Printf("%stimestamp: %d\n", printPrefix, notif.Timestamp)
					fmt.Printf("%sprefix: %s\n", printPrefix, gnmiPathToXPath(notif.Prefix))
					fmt.Printf("%salias: %s\n", printPrefix, notif.Alias)
					for _, upd := range notif.Update {
						if debug {
							log.Printf("DEBUG: update: %+v", upd)
						}
						if upd.Val == nil {
							if debug {
								log.Printf("DEBUG: got a nil val update: %+v", upd)
							}
							continue
						}
						var value interface{}
						var jsondata []byte
						switch upd.Val.Value.(type) {
						case *gnmi.TypedValue_AsciiVal:
							value = upd.Val.GetAsciiVal()
						case *gnmi.TypedValue_BoolVal:
							value = upd.Val.GetBoolVal()
						case *gnmi.TypedValue_BytesVal:
							value = upd.Val.GetBytesVal()
						case *gnmi.TypedValue_DecimalVal:
							value = upd.Val.GetDecimalVal()
						case *gnmi.TypedValue_FloatVal:
							value = upd.Val.GetFloatVal()
						case *gnmi.TypedValue_IntVal:
							value = upd.Val.GetIntVal()
						case *gnmi.TypedValue_StringVal:
							value = upd.Val.GetStringVal()
						case *gnmi.TypedValue_UintVal:
							value = upd.Val.GetUintVal()
						case *gnmi.TypedValue_JsonIetfVal:
							jsondata = upd.Val.GetJsonIetfVal()
						case *gnmi.TypedValue_JsonVal:
							jsondata = upd.Val.GetJsonVal()
						}
						if debug {
							log.Printf("DEBUG: value read from update msg")
							log.Printf("DEBUG: value: (%T) '%v'", value, value)
							log.Printf("DEBUG: jsonData: (%T) '%v'", jsondata, jsondata)
						}
						if len(jsondata) > 0 {
							err = json.Unmarshal(jsondata, &value)
							if err != nil {
								log.Printf("error unmarshaling jsonVal '%s'", string(jsondata))
								continue
							}
							data, err := json.MarshalIndent(value, printPrefix, "  ")
							if err != nil {
								log.Printf("error marshling jsonVal '%s'", value)
								continue
							}
							fmt.Printf("%s%s: (%T) %s\n", printPrefix, gnmiPathToXPath(upd.Path), upd.Val.Value, data)
						} else if value != nil {
							fmt.Printf("%s%s: (%T) %s\n", printPrefix, gnmiPathToXPath(upd.Path), upd.Val.Value, value)
						}
					}
					fmt.Println()
				}
				//fmt.Println()
				lock.Unlock()
			}(addr)
		}
		wg.Wait()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(getCmd)

	getCmd.Flags().StringSliceP("path", "", []string{""}, "get request paths")
	getCmd.Flags().StringP("prefix", "", "", "get request prefix")
	getCmd.Flags().StringP("model", "", "", "get request model")
	getCmd.Flags().StringP("type", "t", "ALL", "the type of data that is requested from the target. one of: ALL, CONFIG, STATE, OPERATIONAL")
	viper.BindPFlag("get-path", getCmd.Flags().Lookup("path"))
	viper.BindPFlag("get-prefix", getCmd.Flags().Lookup("prefix"))
	viper.BindPFlag("get-model", getCmd.Flags().Lookup("model"))
	viper.BindPFlag("get-type", getCmd.Flags().Lookup("type"))
}
