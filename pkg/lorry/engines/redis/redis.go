/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

# This file is part of KubeBlocks project

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
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	viper "github.com/apecloud/kubeblocks/pkg/viperx"
)

const (
	ClusterType         = "cluster"
	NodeType            = "node"
	sentinelUserEnv     = "SENTINEL_USER"
	sentinelPasswordEnv = "SENTINEL_PASSWORD"
)

func ParseClientFromProperties(properties map[string]string, defaultSettings *Settings) (client redis.UniversalClient, settings *Settings, err error) {
	if defaultSettings == nil {
		settings = &Settings{}
	} else {
		settings = defaultSettings
	}
	err = settings.Decode(properties)
	if err != nil {
		return nil, nil, fmt.Errorf("redis client configuration error: %w", err)
	}
	if settings.Failover {
		return newFailoverClient(settings), settings, nil
	}

	return newClient(settings), settings, nil
}

func newFailoverClient(s *Settings) redis.UniversalClient {
	if s == nil {
		return nil
	}
	opts := &redis.FailoverOptions{
		DB:              s.DB,
		MasterName:      s.SentinelMasterName,
		SentinelAddrs:   []string{s.Host},
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

	/* #nosec */
	if s.EnableTLS {
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: s.EnableTLS,
		}
	}

	if s.RedisType == ClusterType {
		opts.SentinelAddrs = strings.Split(s.Host, ",")

		return redis.NewFailoverClusterClient(opts)
	}

	return redis.NewFailoverClient(opts)
}

func newClient(s *Settings) redis.UniversalClient {
	if s == nil {
		return nil
	}
	if s.RedisType == ClusterType {
		options := &redis.ClusterOptions{
			Addrs:           strings.Split(s.Host, ","),
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
		/* #nosec */
		if s.EnableTLS {
			options.TLSConfig = &tls.Config{
				InsecureSkipVerify: s.EnableTLS,
			}
		}

		return redis.NewClusterClient(options)
	}

	options := &redis.Options{
		Addr:            s.Host,
		Password:        s.Password,
		Username:        s.Username,
		DB:              s.DB,
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

	/* #nosec */
	if s.EnableTLS {
		options.TLSConfig = &tls.Config{
			InsecureSkipVerify: s.EnableTLS,
		}
	}

	return redis.NewClient(options)
}

func newSentinelRoleProbeClients(s *Settings, clusterCompName string) []*redis.SentinelClient {
	sentinelAddrs := getSentinelAddrs(clusterCompName)
	sentinelUser, sentinelPassword := getSentinelCredentials(s)
	clients := make([]*redis.SentinelClient, 0, len(sentinelAddrs))
	for _, addr := range sentinelAddrs {
		clients = append(clients, redis.NewSentinelClient(&redis.Options{
			DB:              s.DB,
			Addr:            addr,
			Password:        sentinelPassword,
			Username:        sentinelUser,
			MaxRetries:      s.RedisMaxRetries,
			MaxRetryBackoff: time.Duration(s.RedisMaxRetryInterval),
			MinRetryBackoff: time.Duration(s.RedisMinRetryInterval),
			DialTimeout:     time.Duration(s.DialTimeout),
			ReadTimeout:     time.Duration(s.ReadTimeout),
			WriteTimeout:    time.Duration(s.WriteTimeout),
			PoolSize:        s.PoolSize,
			MinIdleConns:    s.MinIdleConns,
			PoolTimeout:     time.Duration(s.PoolTimeout),
		}))
	}
	return clients
}

func getSentinelAddrs(clusterCompName string) []string {
	sentinelPort := "26379"
	if viper.IsSet("REDIS_SENTINEL_HOST_NETWORK_PORT") {
		sentinelPort = viper.GetString("REDIS_SENTINEL_HOST_NETWORK_PORT")
	}

	if viper.IsSet("SENTINEL_POD_NAME_LIST") && viper.IsSet("SENTINEL_HEADLESS_SERVICE_NAME") {
		sentinelHeadlessServiceName := viper.GetString("SENTINEL_HEADLESS_SERVICE_NAME")
		podNames := strings.Split(viper.GetString("SENTINEL_POD_NAME_LIST"), ",")
		addrs := make([]string, 0, len(podNames))
		for _, podName := range podNames {
			podName = strings.TrimSpace(podName)
			if podName == "" {
				continue
			}
			addrs = append(addrs, fmt.Sprintf("%s.%s:%s", podName, sentinelHeadlessServiceName, sentinelPort))
		}
		if len(addrs) > 0 {
			return addrs
		}
	}

	sentinelHost := fmt.Sprintf("%s-sentinel-headless", clusterCompName)
	if viper.IsSet("SENTINEL_HEADLESS_SERVICE_NAME") {
		sentinelHost = viper.GetString("SENTINEL_HEADLESS_SERVICE_NAME")
	}
	return []string{fmt.Sprintf("%s:%s", sentinelHost, sentinelPort)}
}

func getSentinelCredentials(s *Settings) (string, string) {
	username, password := s.Username, s.Password
	if viper.IsSet(sentinelUserEnv) {
		username = viper.GetString(sentinelUserEnv)
	}
	if viper.IsSet(sentinelPasswordEnv) {
		password = viper.GetString(sentinelPasswordEnv)
	}
	return username, password
}
