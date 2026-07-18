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
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/apecloud/kubeblocks/pkg/lorry/dcs"
	"github.com/apecloud/kubeblocks/pkg/lorry/engines/models"
	viper "github.com/apecloud/kubeblocks/pkg/viperx"
)

func (mgr *Manager) GetReplicaRole(ctx context.Context, _ *dcs.Cluster) (string, error) {
	getRoleFromRedisClient := func() (string, error) {
		return mgr.getLocalRedisRole(ctx)
	}

	// To ensure that the role information obtained through subscription is always delivered.
	if mgr.role != "" && mgr.roleSubscribeUpdateTime+mgr.roleProbePeriod*2 < time.Now().Unix() {
		if mgr.role != models.PRIMARY {
			return mgr.role, nil
		}
		role, err := getRoleFromRedisClient()
		if err != nil {
			return "", err
		}
		if role == models.PRIMARY {
			return models.PRIMARY, nil
		}
		mgr.Logger.Info("subscribed primary role is not local redis master", "localRole", role)
		return models.SECONDARY, nil
	}

	if mgr.sentinelClient == nil {
		return getRoleFromRedisClient()
	}

	masterName, err := mgr.getSentinelMasterName(ctx)
	if err != nil && !mgr.useSentinelMajority() {
		return getRoleFromRedisClient()
	} else if err != nil {
		return "", err
	}

	// if current member is not master from sentinel, just return secondary to avoid double master
	if masterName != mgr.CurrentMemberName {
		return models.SECONDARY, nil
	}
	// Sentinel can temporarily return a stale or split-brain master during node restarts.
	// Only report primary when the local Redis process also confirms it is master.
	role, err := getRoleFromRedisClient()
	if err != nil {
		return "", err
	}
	if role != models.PRIMARY {
		mgr.Logger.Info("sentinel master is not local redis master", "sentinelMaster", masterName, "localRole", role)
		return models.SECONDARY, nil
	}
	return models.PRIMARY, nil
}

func (mgr *Manager) getLocalRedisRole(ctx context.Context) (string, error) {
	result, err := mgr.client.Info(ctx, "Replication").Result()
	if err != nil {
		mgr.Logger.Info("Role query failed", "error", err.Error())
		return "", err
	}
	if parseRedisReplicationRole(result) == models.MASTER {
		return models.PRIMARY, nil
	}
	return models.SECONDARY, nil
}

func (mgr *Manager) IsLeader(ctx context.Context, _ *dcs.Cluster) (bool, error) {
	role, err := mgr.getLocalRedisRole(ctx)
	if err != nil {
		return false, err
	}
	return role == models.PRIMARY, nil
}

func (mgr *Manager) getSentinelMasterName(ctx context.Context) (string, error) {
	if mgr.useSentinelMajority() {
		return mgr.getSentinelMajorityMasterName(ctx)
	}
	masterAddr, err := mgr.sentinelClient.GetMasterAddrByName(ctx, mgr.ClusterCompName).Result()
	if err != nil {
		return "", err
	}
	_, name, err := parseSentinelMaster(masterAddr)
	return name, err
}

func (mgr *Manager) useSentinelMajority() bool {
	return viper.IsSet("SENTINEL_POD_NAME_LIST") && viper.IsSet("SENTINEL_HEADLESS_SERVICE_NAME")
}

func (mgr *Manager) getSentinelMajorityMasterName(ctx context.Context) (string, error) {
	pods := splitCSV(viper.GetString("SENTINEL_POD_NAME_LIST"))
	if len(pods) == 0 {
		return "", fmt.Errorf("empty SENTINEL_POD_NAME_LIST")
	}

	headlessSvc := viper.GetString("SENTINEL_HEADLESS_SERVICE_NAME")
	port := "26379"
	if viper.IsSet("REDIS_SENTINEL_HOST_NETWORK_PORT") {
		port = viper.GetString("REDIS_SENTINEL_HOST_NETWORK_PORT")
	}

	type masterVote struct {
		count int
		name  string
	}
	votes := map[string]masterVote{}
	var lastErr error
	for _, pod := range pods {
		client := mgr.newSentinelClientForAddr(fmt.Sprintf("%s.%s:%s", pod, headlessSvc, port))
		masterAddr, err := client.GetMasterAddrByName(ctx, mgr.ClusterCompName).Result()
		_ = client.Close()
		if err != nil {
			lastErr = err
			mgr.Logger.Info("query sentinel master failed", "sentinel", pod, "error", err.Error())
			continue
		}
		masterKey, masterName, err := parseSentinelMaster(masterAddr)
		if err != nil {
			lastErr = err
			mgr.Logger.Info("query sentinel master returned invalid address", "sentinel", pod, "error", err.Error())
			continue
		}
		vote := votes[masterKey]
		vote.count++
		vote.name = masterName
		votes[masterKey] = vote
	}

	quorum := len(pods)/2 + 1
	var winner masterVote
	for _, vote := range votes {
		if vote.count > winner.count {
			winner = vote
		}
	}
	if winner.count >= quorum {
		return winner.name, nil
	}
	if len(votes) == 0 && lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("redis sentinels do not agree on master: votes=%d quorum=%d", len(votes), quorum)
}

func (mgr *Manager) newSentinelClientForAddr(addr string) *goredis.SentinelClient {
	s := mgr.clientSettings
	opt := &goredis.Options{
		DB:              s.DB,
		Addr:            addr,
		Password:        s.Password,
		Username:        s.Username,
		MaxRetries:      s.RedisMaxRetries,
		MaxRetryBackoff: time.Duration(s.RedisMaxRetryInterval),
		MinRetryBackoff: time.Duration(s.RedisMinRetryInterval),
		DialTimeout:     time.Duration(s.DialTimeout),
		ReadTimeout:     time.Duration(s.ReadTimeout),
		WriteTimeout:    time.Duration(s.WriteTimeout),
		PoolSize:        s.PoolSize,
		MinIdleConns:    s.MinIdleConns,
		PoolTimeout:     time.Duration(s.PoolTimeout),
	}
	return goredis.NewSentinelClient(opt)
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

func parseRedisHostName(host string) string {
	return strings.Split(host, ".")[0]
}

func parseSentinelMaster(masterAddr []string) (string, string, error) {
	if len(masterAddr) < 2 || strings.TrimSpace(masterAddr[0]) == "" || strings.TrimSpace(masterAddr[1]) == "" {
		return "", "", fmt.Errorf("invalid sentinel master address: %v", masterAddr)
	}
	host := strings.TrimSpace(masterAddr[0])
	port := strings.TrimSpace(masterAddr[1])
	return host + ":" + port, parseRedisHostName(host), nil
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	results := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			results = append(results, part)
		}
	}
	return results
}

func (mgr *Manager) SubscribeRoleChange(ctx context.Context) {
	pubSub := mgr.sentinelClient.Subscribe(ctx, "+switch-master")

	// go-redis periodically sends ping messages to test connection health
	// and re-subscribes if ping can not receive for 30 seconds.
	// so we don't need to retry
	ch := pubSub.Channel()
	for msg := range ch {
		// +switch-master <master name> <old ip> <old port> <new ip> <new port>
		masterAddr := strings.Split(msg.Payload, " ")
		masterName := strings.Split(masterAddr[3], ".")[0]

		if masterName == mgr.CurrentMemberName {
			mgr.role = models.PRIMARY
		} else {
			mgr.role = models.SECONDARY
		}
		mgr.roleSubscribeUpdateTime = time.Now().Unix()
	}
}
