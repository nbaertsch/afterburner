package tooling

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/policy"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

type GrantFile struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Protocol      string               `json:"protocol"`
	Revision      int                  `json:"revision"`
	Epoch         uint64               `json:"epoch"`
	Records       []policy.GrantRecord `json:"records"`
	Events        []policy.GrantEvent  `json:"events,omitempty"`
}

func GrantPath(homeRoot string) string { return filepath.Join(homeRoot, "config", "ui-grants.json") }
func EnterprisePolicyPath(homeRoot string) string {
	return filepath.Join(homeRoot, "config", "ui-enterprise-policy.json")
}

func LoadGrantFile(homeRoot string) (GrantFile, error) {
	path := GrantPath(homeRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return GrantFile{SchemaVersion: SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, Epoch: 1}, nil
	}
	if err != nil {
		return GrantFile{}, err
	}
	var file GrantFile
	if err := json.Unmarshal(data, &file); err != nil {
		return GrantFile{}, err
	}
	if file.SchemaVersion != SchemaVersion || file.Protocol != protocol.Protocol || file.Revision != protocol.ProtocolRevision {
		return GrantFile{}, fmt.Errorf("unsupported ui grants file")
	}
	return file, nil
}

func SaveGrantFile(homeRoot string, file GrantFile) error {
	file.SchemaVersion = SchemaVersion
	file.Protocol = protocol.Protocol
	file.Revision = protocol.ProtocolRevision
	if file.Epoch == 0 {
		file.Epoch = 1
	}
	sort.SliceStable(file.Records, func(i, j int) bool {
		return file.Records[i].ExtensionID+file.Records[i].ID < file.Records[j].ExtensionID+file.Records[j].ID
	})
	data, err := MarshalDeterministic(file)
	if err != nil {
		return err
	}
	path := GrantPath(homeRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func Grant(homeRoot, extensionID, capabilityID, resource, reason string) (policy.GrantRecord, error) {
	if extensionID == "" || capabilityID == "" {
		return policy.GrantRecord{}, fmt.Errorf("extension id and capability are required")
	}
	if !validUICapability(capabilityID) {
		return policy.GrantRecord{}, fmt.Errorf("unknown ui capability %q", capabilityID)
	}
	if resource == "" {
		resource = "*"
	}
	if reason == "" {
		reason = "afterburn ui grant"
	}
	file, err := LoadGrantFile(homeRoot)
	if err != nil {
		return policy.GrantRecord{}, err
	}
	file.Epoch++
	record := policy.GrantRecord{ID: grantID(capabilityID, resource), ExtensionID: extensionID, Capability: capability.ID(capabilityID), Resources: []string{resource}, Effect: capability.PolicyAllow, Epoch: file.Epoch, CreatedAt: DeterministicTime, Reason: reason}
	updated := false
	for i, existing := range file.Records {
		if existing.ExtensionID == extensionID && existing.ID == record.ID {
			file.Records[i] = record
			updated = true
			break
		}
	}
	if !updated {
		file.Records = append(file.Records, record)
	}
	file.Events = append(file.Events, policy.GrantEvent{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, Action: policy.ActionGrant, Grant: record, At: DeterministicTime})
	return record, SaveGrantFile(homeRoot, file)
}

func Revoke(homeRoot, extensionID, grantIDValue, reason string) (policy.GrantRecord, bool, error) {
	file, err := LoadGrantFile(homeRoot)
	if err != nil {
		return policy.GrantRecord{}, false, err
	}
	if reason == "" {
		reason = "afterburn ui revoke"
	}
	now := DeterministicTime
	for i, record := range file.Records {
		if record.ExtensionID == extensionID && record.ID == grantIDValue && record.RevokedAt == nil {
			file.Epoch++
			record.Epoch = file.Epoch
			record.RevokedAt = &now
			record.Reason = reason
			file.Records[i] = record
			file.Events = append(file.Events, policy.GrantEvent{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, Action: policy.ActionRevoke, Grant: record, At: now})
			return record, true, SaveGrantFile(homeRoot, file)
		}
	}
	return policy.GrantRecord{}, false, nil
}

func ShowEnterprisePolicy(homeRoot string) (policy.EnterprisePolicy, error) {
	data, err := os.ReadFile(EnterprisePolicyPath(homeRoot))
	if os.IsNotExist(err) {
		return policy.EnterprisePolicy{SchemaVersion: 1, Version: "default", DenyByDefault: true}, nil
	}
	if err != nil {
		return policy.EnterprisePolicy{}, err
	}
	return policy.LoadEnterprisePolicy(data)
}

func SetEnterprisePolicy(homeRoot, sourcePath string) (policy.EnterprisePolicy, error) {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return policy.EnterprisePolicy{}, err
	}
	p, err := policy.LoadEnterprisePolicy(data)
	if err != nil {
		return policy.EnterprisePolicy{}, err
	}
	out, err := MarshalDeterministic(p)
	if err != nil {
		return policy.EnterprisePolicy{}, err
	}
	path := EnterprisePolicyPath(homeRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return policy.EnterprisePolicy{}, err
	}
	return p, os.WriteFile(path, out, 0o600)
}

func grantID(capabilityID, resource string) string {
	base := strings.TrimPrefix(strings.ReplaceAll(capabilityID, ".", "-"), "ui-")
	res := strings.NewReplacer("*", "all", ":", "-", "\\", "-", "/", "-").Replace(resource)
	return base + ":" + res
}

var _ = time.Time{}
