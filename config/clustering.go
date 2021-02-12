package config

import "time"

const (
	defaultTargetWatchTimer   = 10 * time.Second
	defaultServicesWatchTimer = 1 * time.Minute
	defaultLeaderWaitTimer    = 5 * time.Second
)

type clustering struct {
	ClusterName        string                 `mapstructure:"cluster-name,omitempty"`
	InstanceName       string                 `mapstructure:"instance-name,omitempty"`
	ServicesWatchTimer time.Duration          `mapstructure:"services-watch-timer,omitempty"`
	TargetsWatchTimer  time.Duration          `mapstructure:"targets-watch-timer,omitempty"`
	LeaderWaitTimer    time.Duration          `mapstructure:"leader-wait-timer,omitempty"`
	Locker             map[string]interface{} `mapstructure:"locker,omitempty"`
}

func (c *Config) GetClustering() error {
	if !c.FileConfig.IsSet("clustering") {
		return nil
	}
	c.Clustering = new(clustering)
	c.Clustering.ClusterName = c.FileConfig.GetString("clustering/cluster-name")
	c.Clustering.InstanceName = c.FileConfig.GetString("clustering/instance-name")
	c.Clustering.TargetsWatchTimer = c.FileConfig.GetDuration("clustering/targets-watch-timer")
	c.Clustering.ServicesWatchTimer = c.FileConfig.GetDuration("clustering/services-watch-timer")
	c.Clustering.LeaderWaitTimer = c.FileConfig.GetDuration("clustering/leader-wait-timer")
	c.setClusteringDefaults()
	return c.getLocker()
}

func (c *Config) setClusteringDefaults() {
	if c.Clustering.ClusterName == "" {
		c.Clustering.ClusterName = c.LocalFlags.SubscribeClusterName
	}
	if c.Clustering.InstanceName == "" {
		c.Clustering.InstanceName = c.GlobalFlags.InstanceName
	}
	if c.Clustering.TargetsWatchTimer <= 0 {
		c.Clustering.TargetsWatchTimer = defaultTargetWatchTimer
	}
	if c.Clustering.ServicesWatchTimer <= 0 {
		c.Clustering.ServicesWatchTimer = defaultServicesWatchTimer
	}
	if c.Clustering.LeaderWaitTimer <= 0 {
		c.Clustering.LeaderWaitTimer = defaultLeaderWaitTimer
	}
}