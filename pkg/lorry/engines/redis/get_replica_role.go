/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/apecloud/kubeblocks/pkg/lorry/dcs"
	"github.com/apecloud/kubeblocks/pkg/lorry/engines/models"
)

func (mgr *Manager) GetReplicaRole(ctx context.Context, _ *dcs.Cluster) (string, error) {
	if mgr.sentinelClient == nil {
		return mgr.getLocalRedisRole(ctx)
	}

	masterName, err := mgr.getSentinelMajorityMasterName(ctx)
	if err != nil {
		// Sentinel is registered after the Redis component first becomes Running.
		// During that initial bootstrap, the local Redis role is the only available source.
		if errors.Is(err, goredis.Nil) {
			return mgr.getLocalRedisRole(ctx)
		}
		return "", err
	}

	if masterName != mgr.CurrentMemberName {
		return models.SECONDARY, nil
	}
	return mgr.getLocalRedisRole(ctx)
}

func (mgr *Manager) getSentinelMajorityMasterName(ctx context.Context) (string, error) {
	sentinelClients := newSentinelRoleProbeClients(mgr.clientSettings, mgr.ClusterCompName)
	for _, client := range sentinelClients {
		defer client.Close()
	}

	masterNames := make([]string, 0, len(sentinelClients))
	var missingMasterCount int
	for _, client := range sentinelClients {
		masterAddr, err := client.GetMasterAddrByName(ctx, mgr.ClusterCompName).Result()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				missingMasterCount++
				continue
			}
			mgr.Logger.Info("Sentinel master query failed", "error", err.Error())
			continue
		}

		masterName, err := parseSentinelMasterName(masterAddr)
		if err != nil {
			mgr.Logger.Info("Sentinel master address is invalid", "error", err.Error())
			continue
		}
		masterNames = append(masterNames, masterName)
	}

	if len(masterNames) == 0 && missingMasterCount == len(sentinelClients) {
		return "", goredis.Nil
	}
	if masterName, ok := selectMajorityMasterName(masterNames, len(sentinelClients)); ok {
		return masterName, nil
	}
	return "", fmt.Errorf(
		"no sentinel quorum for master, valid votes:%d, sentinel count:%d",
		len(masterNames),
		len(sentinelClients),
	)
}

func (mgr *Manager) getLocalRedisRole(ctx context.Context) (string, error) {
	result, err := mgr.client.Info(ctx, "Replication").Result()
	if err != nil {
		mgr.Logger.Info("Role query failed", "error", err.Error())
		return "", err
	}
	switch role := parseRedisReplicationRole(result); role {
	case models.MASTER:
		return models.PRIMARY, nil
	case models.SLAVE:
		return models.SECONDARY, nil
	default:
		return "", fmt.Errorf("invalid redis replication role %q", role)
	}
}

func parseRedisReplicationRole(info string) string {
	for _, line := range strings.FieldsFunc(info, func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "role:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "role:"))
		}
	}
	return ""
}

func parseSentinelMasterName(masterAddr []string) (string, error) {
	if len(masterAddr) < 2 || strings.TrimSpace(masterAddr[0]) == "" || strings.TrimSpace(masterAddr[1]) == "" {
		return "", fmt.Errorf("invalid sentinel master address: %v", masterAddr)
	}
	host := strings.TrimSpace(masterAddr[0])
	return strings.Split(host, ".")[0], nil
}

func selectMajorityMasterName(masterNames []string, sentinelCount int) (string, bool) {
	if sentinelCount <= 0 {
		return "", false
	}
	quorum := sentinelCount/2 + 1
	counts := make(map[string]int, len(masterNames))
	for _, masterName := range masterNames {
		if masterName == "" {
			continue
		}
		counts[masterName]++
		if counts[masterName] >= quorum {
			return masterName, true
		}
	}
	return "", false
}
