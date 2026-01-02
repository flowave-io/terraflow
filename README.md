# Terraflow

![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/flowave-io/terraflow?filename=go.mod&logo=go)
[![Go Report Card](https://goreportcard.com/badge/github.com/flowave-io/terraflow)](https://goreportcard.com/report/github.com/flowave-io/terraflow)
[![GoDoc](https://godoc.org/github.com/flowave-io/terraflow?status.svg)](https://godoc.org/github.com/flowave-io/terraflow)
[![GitHub Release](https://img.shields.io/github/v/release/flowave-io/terraflow?logo=GitHub&label=Release&color=4fc528)](https://github.com/flowave-io/terraflow/releases/latest)
[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/flowave-io/terraflow/checks.yml?logo=GitHub&label=Checks&color=4fc528)](https://github.com/flowave-io/terraflow/actions/workflows/checks.yml)
[![Discord](https://img.shields.io/discord/1453732604615331904?logo=Discord&logoColor=white&label=Discord&labelColor=667BC4&color=282b30)](https://discord.gg/8dEwNWNv)
[![X (formerly Twitter) Follow](https://img.shields.io/badge/Follow-black?style=flat&logo=x&logoColor=white)](https://x.com/intent/follow?screen_name=flowave_io)

Terraflow is a real-time development solution for Terraform and OpenTofu, letting you explore and understand your infrastructure as you code.

While traditional Terraform debugging feels disconnected, Terraflow introduces a liquid approach, turning Terraform development into a continuously responsive experience.

You edit code, Terraflow reflects the changes.

## Features

**Live Updates**: The console automatically refreshes when you modify `.tf` or `.tfvars` files. Edit your Terraform configuration, and the console immediately reflects the changes.

**Tab Autocompletion**: Press `Tab` to cycle through available completions for variables, locals, resources, modules, and functions. Press `Shift+Tab` to cycle backward through suggestions.

**Command History**: All executed commands are persisted. Use the up and down arrow keys to navigate through your command history across sessions.

**Multiline Expressions**: Paste complex multiline Terraform expressions directly into the console. The console automatically handles formatting and evaluation.

**Suggestions**: As you type, the console displays inline suggestions based on your command history and available Terraform functions. Press the right arrow at the end of a line to accept a suggestion.

## Installation

### From the Binary Releases

1. Download the latest release from the [Releases page](https://github.com/terraflow/terraflow/releases).
2. Unpack it `tar -zxvf terraflow_0.1.1_darwin_arm64.tar.gz`
3. Move the binary from the unpacked directory to its desired destination `mv terraflow_0.1.1_darwin_arm64/bin/terraflow /usr/local/bin/terraflow`

## Usage

### Basic Usage

Run this command in any directory containing Terraform (`.tf`) files to start the interactive console.

```sh
$ terraflow console
```

### Getting Started

1. Navigate to your Terraform project directory:

```sh
$ cd /path/to/your/terraform/project
```

2. Start the console:

```sh
$ terraflow console
```

3. Try evaluating some expressions:

```text
>> var.some_var
"initial"

>> upper("hello world")
"HELLO WORLD"

>> local.environment
"production"
```

4. Exit the console:

```text
>> exit
```

Or press `Ctrl+D`.

### Command-Line Options

```sh
$ terraflow console [options]
```

| Option                 | Description                                                                                                                                                                                                                                                                                                    |
|------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `-var-file=path`       | Set variables in the Terraform configuration from a file. If "terraform.tfvars" or any ".auto.tfvars" files are present, they will be automatically loaded.                                                                                                                                                    |
| `-backend-config=path` | Configuration to be merged with what is in the configuration file's 'backend' block. This can be either a path to an HCL file with key/value assignments (same format as terraform.tfvars) or a 'key=value' format, and can be specified multiple times. The backend type must be in the configuration itself. |
| `-pull-remote-state`   | Pull the remote state from its location.                                                                                                                                                                                                                                                                       |

### Keyboard Shortcuts

| Shortcut           | Action                                    |
|--------------------|-------------------------------------------|
| `Tab`              | Cycle forward through completions         |
| `Shift+Tab`        | Cycle backward through completions        |
| `Right Arrow`      | Accept suggestion                         |
| `Up / Down Arrows` | Navigate command history                  |
| `Ctrl+C`           | Clear current input and show fresh prompt |
| `Ctrl+D` or `exit` | Exit the console                          |

### Examples

**Evaluate variables:**

```sh
$ terraflow console -var-file=production.tfvars
```

```text
>> var.instance_type
"t3.large"

>> var.region
"us-west-2"
```

**Work with remote state:**

```sh
$ terraflow console -backend-config=backend.hcl -pull-remote-state
```

This pulls the current remote state so you can query real deployed resources and data sources.

**Complex expressions:**

The console supports multiline expressions, just paste them in:

```text
>> [for x in ["hello", "world"]:
..   upper(x)
.. ]
[
  "HELLO",
  "WORLD",
]
```

## Contributing to Terraflow

See [Contribution guide](CONTRIBUTING.md) for workflow and guidelines.

## License

See the [LICENSE](LICENSE) file.
