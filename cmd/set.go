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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/gnxi/utils/xpath"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/prototext"
	"gopkg.in/yaml.v2"
)

var vTypes = []string{"json", "json_ietf", "string", "int", "uint", "bool", "decimal", "float", "bytes", "ascii"}

type setRspMsg struct {
	Source    string             `json:"source,omitempty"`
	Timestamp int64              `json:"timestamp,omitempty"`
	Time      time.Time          `json:"time,omitempty"`
	Prefix    string             `json:"prefix,omitempty"`
	Results   []*updateResultMsg `json:"results,omitempty"`
}

type updateResultMsg struct {
	Operation string `json:"operation,omitempty"`
	Path      string `json:"path,omitempty"`
}

type setReqMsg struct {
	Prefix  string       `json:"prefix,omitempty"`
	Delete  []string     `json:"delete,omitempty"`
	Replace []*updateMsg `json:"replace,omitempty"`
	Update  []*updateMsg `json:"update,omitempty"`
	// extension is not implemented
}

type updateMsg struct {
	Path string `json:"path,omitempty"`
	Val  string `json:"val,omitempty"`
}

// setCmd represents the set command
var setCmd = &cobra.Command{
	Use:   "set",
	Short: "run gnmi set on targets",

	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		setupCloseHandler(cancel)
		var err error
		addresses := viper.GetStringSlice("address")
		if len(addresses) == 0 {
			return errors.New("no address provided")
		}
		if len(addresses) > 1 {
			fmt.Println("[warning] running set command on multiple targets")
		}
		prefix := viper.GetString("set-prefix")
		gnmiPrefix, err := xpath.ToGNMIPath(prefix)
		if err != nil {
			return err
		}
		deletes := viper.GetStringSlice("delete")
		updates := viper.GetString("update")
		replaces := viper.GetString("replace")

		updatePaths := viper.GetStringSlice("update-path")
		replacePaths := viper.GetStringSlice("replace-path")
		updateFiles := viper.GetStringSlice("update-file")
		replaceFiles := viper.GetStringSlice("replace-file")
		updateValues := viper.GetStringSlice("update-value")
		replaceValues := viper.GetStringSlice("replace-value")
		delimiter := viper.GetString("delimiter")
		if (len(deletes)+len(updates)+len(replaces)) == 0 && (len(updatePaths)+len(replacePaths)) == 0 {
			return errors.New("no paths provided")
		}
		inlineUpdates := len(updates) > 0
		inlineReplaces := len(replaces) > 0
		useUpdateFile := len(updateFiles) > 0 && len(updateValues) == 0
		useReplaceFile := len(replaceFiles) > 0 && len(replaceValues) == 0
		updateTypes := make([]string, 0)
		replaceTypes := make([]string, 0)

		if viper.GetBool("debug") {
			logger.Printf("deletes(%d)=%v\n", len(deletes), deletes)
			logger.Printf("updates(%d)=%v\n", len(updates), updates)
			logger.Printf("replaces(%d)=%v\n", len(replaces), replaces)
			logger.Printf("delimiter=%v\n", delimiter)
			logger.Printf("updates-paths(%d)=%v\n", len(updatePaths), updatePaths)
			logger.Printf("replaces-paths(%d)=%v\n", len(replacePaths), replacePaths)
			logger.Printf("updates-files(%d)=%v\n", len(updateFiles), updateFiles)
			logger.Printf("replaces-files(%d)=%v\n", len(replaceFiles), replaceFiles)
			logger.Printf("updates-values(%d)=%v\n", len(updateValues), updateValues)
			logger.Printf("replaces-values(%d)=%v\n", len(replaceValues), replaceValues)
		}
		if inlineUpdates && !useUpdateFile {
			updateSlice := strings.Split(updates, delimiter)
			if len(updateSlice) < 3 {
				return fmt.Errorf("'%s' invalid inline update format: %v", updates, err)
			}
			updatePaths = append(updatePaths, updateSlice[0])
			updateTypes = append(updateTypes, updateSlice[1])
			updateValues = append(updateValues, strings.Join(updateSlice[2:], delimiter))
		}
		if inlineReplaces && !useReplaceFile {
			replaceSlice := strings.Split(replaces, delimiter)
			if len(replaceSlice) < 3 {
				return fmt.Errorf("'%s' invalid inline replace format: %v", replaces, err)
			}
			replacePaths = append(replacePaths, replaceSlice[0])
			replaceTypes = append(replaceTypes, replaceSlice[1])
			replaceValues = append(replaceValues, strings.Join(replaceSlice[2:], delimiter))
		}

		if useUpdateFile && !inlineUpdates {
			if len(updatePaths) != len(updateFiles) {
				return errors.New("missing or extra update files")
			}
		} else {
			if len(updatePaths) != len(updateValues) && len(updates) > 0 {
				return errors.New("missing or extra update values")
			}
		}
		if useReplaceFile && !inlineReplaces {
			if len(replacePaths) != len(replaceFiles) {
				return errors.New("missing or extra replace files")
			}
		} else {
			if len(replacePaths) != len(replaceValues) && len(replaces) > 0 {
				return errors.New("missing or extra replace values")
			}
		}

		req := &gnmi.SetRequest{
			Prefix:  gnmiPrefix,
			Delete:  make([]*gnmi.Path, 0, len(deletes)),
			Replace: make([]*gnmi.Update, 0, len(replaces)),
			Update:  make([]*gnmi.Update, 0, len(updates)),
		}
		for _, p := range deletes {
			gnmiPath, err := xpath.ToGNMIPath(strings.TrimSpace(p))
			if err != nil {
				logger.Printf("path '%s' parse error: %v", p, err)
				continue
			}
			req.Delete = append(req.Delete, gnmiPath)
		}
		for i, p := range updatePaths {
			gnmiPath, err := xpath.ToGNMIPath(strings.TrimSpace(p))
			if err != nil {
				logger.Print(err)
			}
			value := new(gnmi.TypedValue)
			if useUpdateFile {
				var updateData []byte
				updateData, err = readFile(updateFiles[i])
				if err != nil {
					logger.Printf("error reading data from file '%s': %v", updateFiles[i], err)
					continue
				}
				value.Value = &gnmi.TypedValue_JsonVal{
					JsonVal: bytes.Trim(updateData, " \r\n\t"),
				}
			} else {
				var vType string
				if len(updateTypes) > i {
					vType = updateTypes[i]
				} else {
					vType = "json"
				}
				switch vType {
				case "json":
					buff := new(bytes.Buffer)
					err = json.NewEncoder(buff).Encode(strings.TrimRight(strings.TrimLeft(updateValues[i], "["), "]"))
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_JsonVal{
						JsonVal: bytes.Trim(buff.Bytes(), " \r\n\t"),
					}
				case "json_ietf":
					buff := new(bytes.Buffer)
					err = json.NewEncoder(buff).Encode(strings.TrimRight(strings.TrimLeft(updateValues[i], "["), "]"))
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_JsonIetfVal{
						JsonIetfVal: bytes.Trim(buff.Bytes(), " \r\n\t"),
					}
				case "ascii":
					value.Value = &gnmi.TypedValue_AsciiVal{
						AsciiVal: updateValues[i],
					}
				case "bool":
					bval, err := strconv.ParseBool(updateValues[i])
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_BoolVal{
						BoolVal: bval,
					}
				case "bytes":
					value.Value = &gnmi.TypedValue_BytesVal{
						BytesVal: []byte(updateValues[i]),
					}
				case "decimal":
					dVal := &gnmi.Decimal64{}
					value.Value = &gnmi.TypedValue_DecimalVal{
						DecimalVal: dVal,
					}
					logger.Println("decimal type not implemented")
					return nil
				case "float":
					f, err := strconv.ParseFloat(updateValues[i], 32)
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_FloatVal{
						FloatVal: float32(f),
					}
				case "int":
					k, err := strconv.ParseInt(updateValues[i], 10, 64)
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_IntVal{
						IntVal: k,
					}
				case "uint":
					u, err := strconv.ParseUint(updateValues[i], 10, 64)
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_UintVal{
						UintVal: u,
					}
				case "string":
					value.Value = &gnmi.TypedValue_StringVal{
						StringVal: updateValues[i],
					}
				default:
					return fmt.Errorf("unknown type '%s', must be one of: %v", vType, vTypes)
				}
			}
			req.Update = append(req.Update, &gnmi.Update{
				Path: gnmiPath,
				Val:  value,
			})
		}
		for i, p := range replacePaths {
			gnmiPath, err := xpath.ToGNMIPath(strings.TrimSpace(p))
			if err != nil {
				logger.Print(err)
			}
			value := new(gnmi.TypedValue)
			if useReplaceFile {
				var replaceData []byte
				replaceData, err = readFile(replaceFiles[i])
				if err != nil {
					logger.Printf("error reading data from file '%s': %v", replaceFiles[i], err)
					continue
				}
				value.Value = &gnmi.TypedValue_JsonVal{
					JsonVal: bytes.Trim(replaceData, " \r\n\t"),
				}
			} else {
				var vType string
				if len(replaceTypes) > i {
					vType = replaceTypes[i]
				} else {
					vType = "json"
				}
				switch vType {
				case "json":
					buff := new(bytes.Buffer)
					err = json.NewEncoder(buff).Encode(strings.TrimRight(strings.TrimLeft(replaceValues[i], "["), "]"))
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_JsonVal{
						JsonVal: bytes.Trim(buff.Bytes(), " \r\n\t"),
					}
				case "json_ietf":
					buff := new(bytes.Buffer)
					err = json.NewEncoder(buff).Encode(strings.TrimRight(strings.TrimLeft(replaceValues[i], "["), "]"))
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_JsonIetfVal{
						JsonIetfVal: bytes.Trim(buff.Bytes(), " \r\n\t"),
					}
				case "ascii":
					value.Value = &gnmi.TypedValue_AsciiVal{
						AsciiVal: replaceValues[i],
					}
				case "bool":
					bval, err := strconv.ParseBool(replaceValues[i])
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_BoolVal{
						BoolVal: bval,
					}
				case "bytes":
					value.Value = &gnmi.TypedValue_BytesVal{
						BytesVal: []byte(replaceValues[i]),
					}
				case "decimal":
					dVal := &gnmi.Decimal64{}
					value.Value = &gnmi.TypedValue_DecimalVal{
						DecimalVal: dVal,
					}
					logger.Println("decimal type not implemented")
					return nil
				case "float":
					f, err := strconv.ParseFloat(replaceValues[i], 32)
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_FloatVal{
						FloatVal: float32(f),
					}
				case "int":
					i, err := strconv.ParseInt(replaceValues[i], 10, 64)
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_IntVal{
						IntVal: i,
					}
				case "uint":
					i, err := strconv.ParseUint(replaceValues[i], 10, 64)
					if err != nil {
						return err
					}
					value.Value = &gnmi.TypedValue_UintVal{
						UintVal: i,
					}
				case "string":
					value.Value = &gnmi.TypedValue_StringVal{
						StringVal: replaceValues[i],
					}
				default:
					return fmt.Errorf("unknown type '%s', must be one of: %v", vType, vTypes)
				}
			}
			req.Replace = append(req.Replace, &gnmi.Update{
				Path: gnmiPath,
				Val:  value,
			})
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
		wg := new(sync.WaitGroup)
		wg.Add(len(addresses))
		lock := new(sync.Mutex)
		for _, addr := range addresses {
			go setRequest(ctx, req, addr, username, password, wg, lock)
		}
		wg.Wait()
		return nil
	},
}

func setRequest(ctx context.Context, req *gnmi.SetRequest, address, username, password string, wg *sync.WaitGroup, lock *sync.Mutex) {
	defer wg.Done()
	_, _, err := net.SplitHostPort(address)
	if err != nil {
		if strings.Contains(err.Error(), "missing port in address") {
			address = net.JoinHostPort(address, defaultGrpcPort)
		} else {
			logger.Printf("error parsing address '%s': %v", address, err)
			return
		}
	}
	conn, err := createGrpcConn(address)
	if err != nil {
		logger.Printf("connection to %s failed: %v", address, err)
		return
	}
	client := gnmi.NewGNMIClient(conn)
	nctx, cancel := context.WithCancel(ctx)
	defer cancel()
	nctx = metadata.AppendToOutgoingContext(nctx, "username", username, "password", password)

	addresses := viper.GetStringSlice("address")
	printPrefix := ""
	if len(addresses) > 1 && !viper.GetBool("no-prefix") {
		printPrefix = fmt.Sprintf("[%s] ", address)
	}
	lock.Lock()
	defer lock.Unlock()
	if viper.GetBool("print-request") {
		printSetRequest(printPrefix, req)
	}
	logger.Printf("sending gNMI SetRequest: prefix='%v', delete='%v', replace='%v', update='%v', extension='%v' to %s", req.Prefix, req.Delete, req.Replace, req.Update, req.Extension, address)
	response, err := client.Set(nctx, req)
	if err != nil {
		logger.Printf("error sending set request: %v", err)
		return
	}
	printSetResponse(printPrefix, address, response)
}

// readFile reads a json or yaml file. the the file is .yaml, converts it to json and returns []byte and an error
func readFile(name string) ([]byte, error) {
	data, err := ioutil.ReadFile(name)
	if err != nil {
		return nil, err
	}
	switch filepath.Ext(name) {
	case ".json":
		return data, err
	case ".yaml", ".yml":
		var out interface{}
		err = yaml.Unmarshal(data, &out)
		if err != nil {
			return nil, err
		}
		newStruct := convert(out)
		newData, err := json.Marshal(newStruct)
		if err != nil {
			return nil, err
		}
		return newData, nil
	default:
		return nil, fmt.Errorf("unsupported file format %s", filepath.Ext(name))
	}
}
func convert(i interface{}) interface{} {
	switch x := i.(type) {
	case map[interface{}]interface{}:
		nm := map[string]interface{}{}
		for k, v := range x {
			nm[k.(string)] = convert(v)
		}
		return nm
	case []interface{}:
		for i, v := range x {
			x[i] = convert(v)
		}
	}
	return i
}
func printSetRequest(printPrefix string, request *gnmi.SetRequest) {
	if viper.GetString("format") == "textproto" {
		fmt.Printf("%s\n", indent("  ", prototext.Format(request)))
		return
	}
	fmt.Printf("%sSet Request: \n", printPrefix)
	req := new(setReqMsg)
	req.Prefix = gnmiPathToXPath(request.Prefix)
	req.Delete = make([]string, 0, len(request.Delete))
	req.Replace = make([]*updateMsg, 0, len(request.Replace))
	req.Update = make([]*updateMsg, 0, len(request.Update))

	for _, del := range request.Delete {
		p := gnmiPathToXPath(del)
		req.Delete = append(req.Delete, p)
	}

	for _, upd := range request.Replace {
		updMsg := new(updateMsg)
		updMsg.Path = gnmiPathToXPath(upd.Path)
		updMsg.Val = fmt.Sprintf("%s", upd.Val)
		req.Replace = append(req.Replace, updMsg)
	}

	for _, upd := range request.Update {
		updMsg := new(updateMsg)
		updMsg.Path = gnmiPathToXPath(upd.Path)
		updMsg.Val = fmt.Sprintf("%s", upd.Val)
		req.Update = append(req.Update, updMsg)
	}

	b, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		fmt.Println("failed marshaling the set request", err)
		return
	}
	fmt.Println(string(b))
}
func printSetResponse(printPrefix, address string, response *gnmi.SetResponse) {
	if viper.GetString("format") == "textproto" {
		fmt.Printf("%s\n", indent(printPrefix, prototext.Format(response)))
		return
	}
	rsp := new(setRspMsg)
	rsp.Prefix = gnmiPathToXPath(response.Prefix)
	rsp.Timestamp = response.Timestamp
	rsp.Time = time.Unix(0, response.Timestamp)
	rsp.Results = make([]*updateResultMsg, 0, len(response.Response))
	rsp.Source = address
	for _, u := range response.Response {
		r := new(updateResultMsg)
		r.Operation = u.Op.String()
		r.Path = gnmiPathToXPath(u.Path)
		rsp.Results = append(rsp.Results, r)
	}
	b, err := json.MarshalIndent(rsp, "", "  ")
	if err != nil {
		fmt.Printf("failed marshaling the set response from '%s': %v", address, err)
		return
	}
	fmt.Println(string(b))
}

func init() {
	rootCmd.AddCommand(setCmd)

	setCmd.Flags().StringP("prefix", "", "", "set request prefix")

	setCmd.Flags().StringSliceP("delete", "", []string{}, "set request path to be deleted")

	setCmd.Flags().StringP("replace", "", "", fmt.Sprintf("set request path:::type:::value to be replaced, type must be one of %v", vTypes))
	setCmd.Flags().StringP("update", "", "", fmt.Sprintf("set request path:::type:::value to be updated, type must be one of %v", vTypes))

	setCmd.Flags().StringSliceP("replace-path", "", []string{""}, "set request path to be replaced")
	setCmd.Flags().StringSliceP("update-path", "", []string{""}, "set request path to be updated")
	setCmd.Flags().StringSliceP("update-file", "", []string{""}, "set update request value in json file")
	setCmd.Flags().StringSliceP("replace-file", "", []string{""}, "set replace request value in json file")
	setCmd.Flags().StringSliceP("update-value", "", []string{""}, "set update request value")
	setCmd.Flags().StringSliceP("replace-value", "", []string{""}, "set replace request value")
	setCmd.Flags().StringP("delimiter", "", ":::", "set update/replace delimiter between path,type,value")
	setCmd.Flags().BoolP("print-request", "", false, "print set request as well as the response")

	viper.BindPFlag("set-prefix", setCmd.Flags().Lookup("prefix"))
	viper.BindPFlag("delete", setCmd.Flags().Lookup("delete"))
	viper.BindPFlag("replace", setCmd.Flags().Lookup("replace"))
	viper.BindPFlag("update", setCmd.Flags().Lookup("update"))
	viper.BindPFlag("update-path", setCmd.Flags().Lookup("update-path"))
	viper.BindPFlag("replace-path", setCmd.Flags().Lookup("replace-path"))
	viper.BindPFlag("update-file", setCmd.Flags().Lookup("update-file"))
	viper.BindPFlag("replace-file", setCmd.Flags().Lookup("replace-file"))
	viper.BindPFlag("update-value", setCmd.Flags().Lookup("update-value"))
	viper.BindPFlag("replace-value", setCmd.Flags().Lookup("replace-value"))
	viper.BindPFlag("delimiter", setCmd.Flags().Lookup("delimiter"))
	viper.BindPFlag("print-request", setCmd.Flags().Lookup("print-request"))
}
