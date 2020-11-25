### address
The address flag `[-a | --address]` is used to specify the target's gNMI server address in address:port format, for e.g: `192.168.113.11:57400`

Multiple target addresses can be specified, either as comma separated values:
```
gnmic --address 192.168.113.11:57400,192.168.113.12:57400 
```
or by using the `--address` flag multiple times:
```
gnmic -a 192.168.113.11:57400 --address 192.168.113.12:57400
```

### config
The `--config` flag specifies the location of a configuration file that `gnmic` will read. Defaults to `$HOME/gnmic.yaml`.

### debug
The debug flag `[-d | --debug]` enables the printing of extra information when sending/receiving an RPC

### encoding
The encoding flag `[-e | --encoding]` is used to specify the [gNMI encoding](https://github.com/openconfig/reference/blob/master/rpc/gnmi/gnmi-specification.md#23-structured-data-types) of the Update part of a [Notification](https://github.com/openconfig/reference/blob/master/rpc/gnmi/gnmi-specification.md#21-reusable-notification-message-format) message.

It is case insensitive and must be one of: JSON, BYTES, PROTO, ASCII, JSON_IETF

### format
Five output formats can be configured by means of the `--format` flag. `[proto, protojson, prototext, json, event]` The default format is `json`.

The `proto` format outputs the gnmi message as raw bytes, this value is not allowed when the output type is file (file system, stdout or stderr) see [outputs](advanced/multi_outputs/output_intro.md)

The `prototext` and `protojson` formats are the message representation as defined in [prototext](https://godoc.org/google.golang.org/protobuf/encoding/prototext) and [protojson](https://godoc.org/google.golang.org/protobuf/encoding/protojson)

The `event` format emits the received gNMI SubscribeResponse updates and deletes as a list of events tagged with the keys present in the subscribe path (as well as some metadata) and a timestamp

Here goes an example of the same response emitted to stdout in the respective formats:

=== "protojson"
    ```json
    {
      "update": {
      "timestamp": "1595584408456503938",
      "prefix": {
        "elem": [
          {
            "name": "state"
          },
          {
            "name": "system"
          },
          {
            "name": "version"
          }
        ]
      },
        "update": [
          {
            "path": {
              "elem": [
                {
                 "name": "version-string"
               }
              ]
            },
            "val": {
              "stringVal": "TiMOS-B-20.5.R1 both/x86_64 Nokia 7750 SR Copyright (c) 2000-2020 Nokia.\r\nAll rights reserved. All use subject to applicable license agreements.\r\nBuilt on Wed May 13 14:08:50 PDT 2020 by builder in /builds/c/205B/R1/panos/main/sros"
            }
          }
        ]
      }
    }
    ```
=== "prototext"
    ```yaml
    update: {
      timestamp: 1595584168675434221
      prefix: {
        elem: {
          name: "state"
        }
        elem: {
          name: "system"
        }
        elem: {
          name: "version"
        }
      }
      update: {
        path: {
          elem: {
            name: "version-string"
          }
        }
        val: {
          string_val: "TiMOS-B-20.5.R1 both/x86_64 Nokia 7750 SR Copyright (c) 2000-2020 Nokia.\r\nAll rights reserved. All use subject to applicable license agreements.\r\nBuilt on Wed May 13 14:08:50 PDT 2020 by builder in /builds/c/205B/R1/panos/main/sros"
        }
      }
    }
    ```
=== "json"
    ```json
    {
      "source": "172.17.0.100:57400",
      "subscription-name": "default",
      "timestamp": 1595584326775141151,
      "time": "2020-07-24T17:52:06.775141151+08:00",
      "prefix": "state/system/version",
      "updates": [
        {
          "Path": "version-string",
          "values": {
            "version-string": "TiMOS-B-20.5.R1 both/x86_64 Nokia 7750 SR Copyright (c) 2000-2020 Nokia.\r\nAll rights reserved. All use subject to applicable license agreements.\r\nBuilt on Wed May 13 14:08:50 PDT 2020 by builder in /builds/c/205B/R1/panos/main/sros"
          }
        }
      ]
    }
    ```
=== "event"
    ```json
    [
      {
        "name": "default",
        "timestamp": 1595584587725708234,
        "tags": {
          "source": "172.17.0.100:57400",
          "subscription-name": "default"
        },
        "values": {
          "/state/system/version/version-string": "TiMOS-B-20.5.R1 both/x86_64 Nokia 7750 SR Copyright (c) 2000-2020 Nokia.\r\nAll rights reserved. All use subject to applicable license agreements.\r\nBuilt on Wed May 13 14:08:50 PDT 2020 by builder in /builds/c/205B/R1/panos/main/sros"
        }
      }
    ]
    ```

### insecure
The insecure flag `[--insecure]` is used to indicate that the client wishes to establish an non-TLS enabled gRPC connection.

To disable certificate validation in a TLS-enabled connection use [`skip-verify`](#skip-verify) flag.

### log
The `--log` flag enables log messages to appear on stderr output. By default logging is disabled.

### log-file
The log-file flag `[--log-file <path>]` sets the log output to a file referenced by the path. This flag supersede the `--log` flag

### no-prefix
The no prefix flag `[--no-prefix]` disables prefixing the json formatted responses with `[ip:port]` string.

Note that in case a single target is specified, the prefix is not added.

### password
The password flag `[-p | --password]` is used to specify the target password as part of the user credentials. If omitted, the password input prompt is used to provide the password.

Note that in case multiple targets are used, all should use the same credentials.

### prometheus-address
The prometheus-address flag `[--prometheus-address]` allows starting a prometheus server that can be scraped by a prometheus client. It exposes metrics like memory, CPU and file descriptor usage.

### proxy-from-env
The proxy-from-env flag `[--proxy-from-env]` indicates that the gnmic should use the HTTP/HTTPS proxy addresses defined in the environment variables `http_proxy` and `https_proxy` to reach the targets specified using the `--address` flag.

### retry
The retry flag `[--retry] specifies the wait time before each retry.

Valid formats: 10s, 1m30s, 1h.  Defaults to 10s

### skip-verify
The skip verify flag `[--skip-verify]` indicates that the target should skip the signature verification steps, in case a secure connection is used.  

### timeout
The timeout flag `[--timeout]` specifies the gRPC timeout after which the connection attempt fails.

Valid formats: 10s, 1m30s, 1h.  Defaults to 10s

### tls-ca
The TLS CA flag `[--tls-ca]` specifies the root certificates for verifying server certificates encoded in PEM format.

### tls-cert
The tls cert flag `[--tls-cert]` specifies the public key for the client encoded in PEM format

### tls-key
The tls key flag `[--tls-key]` specifies the private key for the client encoded in PEM format

### tls-max-version
The tls max version flag `[--tls-max-version]` specifies the maximum supported TLS version supported by gNMIc when creating a secure gRPC connection

### tls-min-version
The tls min version flag `[--tls-min-version]` specifies the minimum supported TLS version supported by gNMIc when creating a secure gRPC connection

### tls-version
The tls version flag `[--tls-version]` specifies a single supported TLS version gNMIc when creating a secure gRPC connection.

This flag overwrites the previously listed flags `--tls-max-version` and `--tls-min-version`.

### username
The username flag `[-u | --username]` is used to specify the target username as part of the user credentials. If omitted, the input prompt is used to provide the username.
