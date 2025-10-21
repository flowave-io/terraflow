package terraform

import (
	"bytes"
	"encoding/json"
	"log"
	"os/exec"
	"regexp"

	gv "github.com/hashicorp/go-version"
)

const minTerraformVersion = "0.13.0"

type tfVersionJSON struct {
	TerraformVersion string `json:"terraform_version"`
}

// CheckVersionWarn attempts to read the installed Terraform/OpenTofu version and
// logs a warning if it is older than the recommended minimum. It never exits.
func CheckVersionWarn() {
	// Try JSON first (Terraform >= 0.15)
	var versionStr string
	if out, err := exec.Command("terraform", "version", "-json").Output(); err == nil {
		var v tfVersionJSON
		if json.Unmarshal(out, &v) == nil && v.TerraformVersion != "" {
			versionStr = v.TerraformVersion
		}
	}
	if versionStr == "" {
		// Fallback to parsing plain text: "Terraform v1.5.7" or "OpenTofu v1.8.0"
		if out, err := exec.Command("terraform", "version").Output(); err == nil {
			// Extract first semantic version
			re := regexp.MustCompile(`v([0-9]+\.[0-9]+\.[0-9]+)`) // v1.5.7
			m := re.FindSubmatch(out)
			if len(m) == 2 {
				versionStr = string(m[1])
			} else {
				// Some distros print without v prefix
				re2 := regexp.MustCompile(`\b([0-9]+\.[0-9]+\.[0-9]+)\b`)
				m2 := re2.FindSubmatch(out)
				if len(m2) == 2 {
					versionStr = string(m2[1])
				}
			}
		}
	}
	if versionStr == "" {
		return
	}
	minV, err1 := gv.NewVersion(minTerraformVersion)
	curV, err2 := gv.NewVersion(versionStr)
	if err1 != nil || err2 != nil {
		return
	}
	if curV.LessThan(minV) {
		var buf bytes.Buffer
		buf.WriteString("Warning: Terraform/OpenTofu version ")
		buf.WriteString(curV.String())
		buf.WriteString(" is older than recommended minimum ")
		buf.WriteString(minV.String())
		buf.WriteString(". Some features may be limited.")
		log.Print(buf.String())
	}
}
