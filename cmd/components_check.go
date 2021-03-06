// Copyright © 2017 Mesosphere Inc. <http://mesosphere.com>
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
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dcos/dcos-checks/client"
	"github.com/dcos/dcos-go/dcos"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	// master node has a 3dt instance running on TCP port 1050.
	// ee version has 3dt running via unix socket on both master and agent nodes,
	// depending on security option. Ports 80 or 443 are using accordingly.
	dcosDiagnosticsMasterHTTPPort = 1050
	adminrouterMasterHTTPSPort    = 443

	// agent node runs 3dt via unix socket and is available though the agent
	// adminrouter HTTP TCP port 61001 or HTTPS 61002.
	adminrouterAgentHTTPPort  = 61001
	adminrouterAgentHTTPSPort = 61002

	httpScheme  = "http"
	httpsScheme = "https"
)

var (
	healthURLPrefix string
)

// componentsCmd represents the systemd health check
var componentsCmd = &cobra.Command{
	Use:   "components",
	Short: "Check DC/OS components",
	Long: `Check DC/OS components health by making a GET request to dcos-3dt service
and validating the health field:

/system/health/v1 is the local endpoint. The response structure is the following
{
  "units": ["unit1", ...]
}
`,
	Run: func(cmd *cobra.Command, args []string) {
		RunCheck(NewComponentCheck("DC/OS components health check"))
	},
}

func init() {
	RootCmd.AddCommand(componentsCmd)
	componentsCmd.Flags().StringVarP(&healthURLPrefix, "health-url", "u", "/system/health/v1", "Set dcos-diagnostics health url")
}

type diagnosticsResponse struct {
	Units []struct {
		ID          string `json:"id"`
		Health      int    `json:"health"`
		Output      string `json:"output"`
		Description string `json:"description"`
		Help        string `json:"help"`
		Name        string `json:"name"`
	} `json:"units"`
}

func (d *diagnosticsResponse) checkHealth() ([]string, int) {
	var errorList []string
	for _, unit := range d.Units {
		if unit.Health != statusOK {
			errorList = append(errorList, fmt.Sprintf("component %s has health status %d", unit.Name, unit.Health))
		}
	}
	retCode := statusOK
	if len(errorList) > 0 {
		retCode = statusFailure
	}
	return errorList, retCode
}

// ComponentCheck validates that all systemd units are healthy by making a GET request
// to dcos-diagnostics endpoint /system/health/v1 on the localhost.
// In open DC/OS 3dt listens port 1050 on master nodes. On agent nodes, 3dt uses socket activation to bind on
// unix socket. Adminrouter is used to make a reverse proxy.
type ComponentCheck struct {
	Name string
}

// Run invokes a systemd check and return error output, exit code and error.
func (c *ComponentCheck) Run(ctx context.Context, cfg *CLIConfigFlags) (string, int, error) {
	httpClient, err := client.NewClient(cfg.IAMConfig, cfg.CACert)
	if err != nil {
		return "", statusUnknown, errors.Wrap(err, "unable to create HTTP client")
	}

	url, err := c.getHealthURL(httpClient, healthURLPrefix, cfg)
	if err != nil {
		return "", 0, err
	}
	logrus.Debugf("GET %s", url)
	req, err := http.NewRequest("GET", url.String(), nil)
	if err != nil {
		return "", statusUnknown, errors.Wrap(err, "unable to create a new HTTP request")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", statusUnknown, errors.Wrapf(err, "unable to execute GET %s", healthURLPrefix)
	}
	defer resp.Body.Close()

	var dr diagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return "", statusUnknown, errors.Wrap(err, "unable to unmarshal diagnostics response")
	}

	errorList, retCode := dr.checkHealth()
	return strings.Join(errorList, "\n"), retCode, nil
}

// ID returns a unique check identifier.
func (c *ComponentCheck) ID() string {
	return c.Name
}

func (c *ComponentCheck) getHealthURL(httpClient *http.Client, path string, cfg *CLIConfigFlags) (*url.URL, error) {
	portsMap := map[string]map[bool]int{
		dcos.RoleMaster: {
			true:  adminrouterMasterHTTPSPort,
			false: dcosDiagnosticsMasterHTTPPort,
		},
		dcos.RoleAgent: {
			true:  adminrouterAgentHTTPSPort,
			false: adminrouterAgentHTTPPort,
		},
		dcos.RoleAgentPublic: {
			true:  adminrouterAgentHTTPSPort,
			false: adminrouterAgentHTTPPort,
		},
	}

	port, ok := portsMap[cfg.Role][cfg.ForceTLS]
	if !ok {
		return nil, errors.Errorf("invalid role %s", cfg.Role)
	}

	scheme := httpScheme
	if cfg.ForceTLS {
		scheme = httpsScheme
	}

	ip, err := cfg.IP(httpClient)
	if err != nil {
		return nil, err
	}

	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(ip.String(), strconv.Itoa(port)),
		Path:   path,
	}, nil
}
