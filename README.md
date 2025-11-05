# pdbCheck

A tool to analyze NodeGroups and PodDisruptionBudgets (PDB) for potential violations during cluster updates.

## Purpose

This program analyzes OpenShift/Kubernetes clusters to identify potential PodDisruptionBudget violations that could occur when NodeGroups (Machine Config Pools) are updated. It helps cluster administrators understand which PDBs might be violated during rolling updates and provides interactive remediation options.

## Features

- **NodeGroup Analysis**: Groups nodes by Machine Config Pools (MCPs) and analyzes their serial upgrade settings
- **PDB Violation Detection**: Identifies scenarios where taking down a NodeGroup would violate PDB constraints
- **Interactive Remediation**: Option to interactively fix violations by safely deleting pods
- **Verbose Reporting**: Detailed output including serial upgrade considerations

## Usage

```bash
./pdbCheck -kubeconfig /path/to/kubeconfig [options]
```

### Options

- `-kubeconfig string`: Path to kubeconfig file (defaults to KUBECONFIG env var)
- `-all`: Include violations from serial upgrade MCPs (maxUnavailable=1) in results
- `-verbose`: Enable verbose output for detailed reporting
- `-fix-violations`: Enable interactive mode to fix PDB violations by deleting pods
- `-loglevel string`: Set log level (debug|info|warn|error)
- `-debug`: Set log level to debug

### Examples

Basic analysis:
```bash
./pdbCheck -kubeconfig ~/.kube/config
```

Include all violations (including serial upgrade MCPs):
```bash
./pdbCheck -kubeconfig ~/.kube/config -all
```

Verbose analysis with detailed output:
```bash
./pdbCheck -kubeconfig ~/.kube/config -verbose
```

Interactive remediation mode:
```bash
./pdbCheck -kubeconfig ~/.kube/config -fix-violations
```

## How it Works

1. **NodeGroup Discovery**: Discovers Machine Config Pools and groups nodes accordingly
2. **PDB Analysis**: Finds all PodDisruptionBudgets in the cluster and their associated pods
3. **Violation Detection**: Calculates whether taking down any NodeGroup would violate PDB constraints
4. **Serial Upgrade Handling**: Considers whether NodeGroups have serial upgrade settings (maxUnavailable=1)
5. **Reporting**: Provides detailed output of potential violations and remediation options

## Build

```bash
go build -o pdbCheck main.go
```

## Test

```bash
go test -v
```