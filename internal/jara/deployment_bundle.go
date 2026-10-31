//go:build !jara_server

//go:generate go run ../../scripts/build_deployment_bundle.go ../..

package jara

import _ "embed"

//go:embed deploy_bundle.tar.gz
var bundledDeployment []byte

func deploymentBundle() []byte { return bundledDeployment }
