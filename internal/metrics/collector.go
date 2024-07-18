package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/xperimental/nextcloud-exporter/internal/client"
	"github.com/xperimental/nextcloud-exporter/serverinfo"
)

const (
	metricPrefix = "nextcloud_"

	labelErrorCauseOther     = "other"
	labelErrorCauseAuth      = "auth"
	labelErrorCauseRatelimit = "ratelimit"
)

var (
	systemInfoDesc = prometheus.NewDesc(
		metricPrefix+"system_info",
		"Contains meta information about Nextcloud as labels. Value is always 1.",
		[]string{"version"}, nil)
	systemUpdateAvailableDesc = prometheus.NewDesc(
		metricPrefix+"system_update_available",
		"Contains information whether a system update is available (0 = no, 1 = yes). The version label contains the latest available nextcloud version.",
		[]string{"version", "available_version"}, nil)
	appsInstalledDesc = prometheus.NewDesc(
		metricPrefix+"apps_installed_total",
		"Number of currently installed apps",
		nil, nil)
	appsUpdatesDesc = prometheus.NewDesc(
		metricPrefix+"apps_updates_available_total",
		"Number of apps that have available updates",
		nil, nil)
	usersDesc = prometheus.NewDesc(
		metricPrefix+"users_total",
		"Number of users of the instance.",
		nil, nil)
	filesDesc = prometheus.NewDesc(
		metricPrefix+"files_total",
		"Number of files served by the instance.",
		nil, nil)
	freeSpaceDesc = prometheus.NewDesc(
		metricPrefix+"free_space_bytes",
		"Free disk space in data directory in bytes.",
		nil, nil)
	sharesDesc = prometheus.NewDesc(
		metricPrefix+"shares_total",
		"Number of shares by type.",
		[]string{"type"}, nil)
	federationsDesc = prometheus.NewDesc(
		metricPrefix+"shares_federated_total",
		"Number of federated shares by direction.",
		[]string{"direction"}, nil)
	activeUsersDesc = prometheus.NewDesc(
		metricPrefix+"active_users_total",
		"Number of active users for the last five minutes.",
		nil, nil)
	hourlyActiveUsersDesc = prometheus.NewDesc(
		metricPrefix+"active_users_hourly_total",
		"Number of active users in the last hour.",
		nil, nil)
	dailyActiveUsersDesc = prometheus.NewDesc(
		metricPrefix+"active_users_daily_total",
		"Number of active users in the last 24 hours.",
		nil, nil)
	phpInfoDesc = prometheus.NewDesc(
		metricPrefix+"php_info",
		"Contains meta information about PHP as labels. Value is always 1.",
		[]string{"version"}, nil)
	phpMemoryLimitDesc = prometheus.NewDesc(
		metricPrefix+"php_memory_limit_bytes",
		"Configured PHP memory limit in bytes.",
		nil, nil)
	phpMaxUploadSizeDesc = prometheus.NewDesc(
		metricPrefix+"php_upload_max_size_bytes",
		"Configured maximum upload size in bytes.",
		nil, nil)
	databaseInfoDesc = prometheus.NewDesc(
		metricPrefix+"database_info",
		"Contains meta information about the database as labels. Value is always 1.",
		[]string{"version", "type"}, nil)
	databaseSizeDesc = prometheus.NewDesc(
		metricPrefix+"database_size_bytes",
		"Size of database in bytes as reported from engine.",
		nil, nil)
)

type nextcloudCollector struct {
	log           logrus.FieldLogger
	infoClient    client.InfoClient
	appsMetrics   bool
	updateMetrics bool

	upMetric           prometheus.Gauge
	scrapeErrorsMetric *prometheus.CounterVec
}

func RegisterCollector(log logrus.FieldLogger, infoClient client.InfoClient, appsMetrics bool, updateMetrics bool) error {
	c := &nextcloudCollector{
		log:           log,
		infoClient:    infoClient,
		appsMetrics:   appsMetrics,
		updateMetrics: updateMetrics,

		upMetric: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: metricPrefix + "up",
			Help: "Indicates if the metrics could be scraped by the exporter.",
		}),
		scrapeErrorsMetric: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: metricPrefix + "scrape_errors_total",
			Help: "Counts the number of scrape errors by this collector.",
		}, []string{"cause"}),
	}

	return prometheus.Register(c)
}

func (c *nextcloudCollector) Describe(ch chan<- *prometheus.Desc) {
	c.upMetric.Describe(ch)
	c.scrapeErrorsMetric.Describe(ch)
	ch <- usersDesc
	ch <- filesDesc
	ch <- freeSpaceDesc
	ch <- sharesDesc
	ch <- federationsDesc
	ch <- activeUsersDesc
	ch <- hourlyActiveUsersDesc
	ch <- dailyActiveUsersDesc
}

func (c *nextcloudCollector) Collect(ch chan<- prometheus.Metric) {
	if err := c.collectNextcloud(ch); err != nil {
		c.log.Errorf("Error during scrape: %s", err)

		cause := labelErrorCauseOther
		if err == client.ErrNotAuthorized {
			cause = labelErrorCauseAuth
		} else if err == client.ErrRatelimit {
			cause = labelErrorCauseRatelimit
		}
		c.scrapeErrorsMetric.WithLabelValues(cause).Inc()
		c.upMetric.Set(0)
	} else {
		c.upMetric.Set(1)
	}

	c.upMetric.Collect(ch)
	c.scrapeErrorsMetric.Collect(ch)
}

func (c *nextcloudCollector) collectNextcloud(ch chan<- prometheus.Metric) error {
	status, err := c.infoClient()
	if err != nil {
		return err
	}

	return readMetrics(ch, status, c.appsMetrics, c.updateMetrics)
}

func readMetrics(ch chan<- prometheus.Metric, status *serverinfo.ServerInfo, appsMetrics bool, updateMetrics bool) error {
	if err := collectSimpleMetrics(ch, status, appsMetrics); err != nil {
		return err
	}

	if updateMetrics {
		if err := collectUpdate(ch, status); err != nil {
			return err
		}
	}

	if err := collectShares(ch, status.Data.Nextcloud.Shares); err != nil {
		return err
	}

	if err := collectFederatedShares(ch, status.Data.Nextcloud.Shares); err != nil {
		return err
	}

	systemInfo := []string{
		status.Data.Nextcloud.System.Version,
	}
	if err := collectInfoMetric(ch, systemInfoDesc, systemInfo); err != nil {
		return err
	}

	phpInfo := []string{
		status.Data.Server.PHP.Version,
	}
	if err := collectInfoMetric(ch, phpInfoDesc, phpInfo); err != nil {
		return err
	}

	databaseInfo := []string{
		status.Data.Server.Database.Version,
		status.Data.Server.Database.Type,
	}
	if err := collectInfoMetric(ch, databaseInfoDesc, databaseInfo); err != nil {
		return err
	}

	return nil
}

type simpleMetric struct {
	desc  *prometheus.Desc
	value float64
}

func collectSimpleMetrics(ch chan<- prometheus.Metric, status *serverinfo.ServerInfo, appsMetrics bool) error {
	metrics := []simpleMetric{
		{
			desc:  usersDesc,
			value: float64(status.Data.Nextcloud.Storage.Users),
		},
		{
			desc:  filesDesc,
			value: float64(status.Data.Nextcloud.Storage.Files),
		},
		{
			desc:  freeSpaceDesc,
			value: status.Data.Nextcloud.System.FreeSpace,
		},
		{
			desc:  activeUsersDesc,
			value: float64(status.Data.ActiveUsers.Last5Minutes),
		},
		{
			desc:  hourlyActiveUsersDesc,
			value: float64(status.Data.ActiveUsers.LastHour),
		},
		{
			desc:  dailyActiveUsersDesc,
			value: float64(status.Data.ActiveUsers.LastDay),
		},
		{
			desc:  phpMemoryLimitDesc,
			value: float64(status.Data.Server.PHP.MemoryLimit),
		},
		{
			desc:  phpMaxUploadSizeDesc,
			value: float64(status.Data.Server.PHP.UploadMaxFilesize),
		},
		{
			desc:  databaseSizeDesc,
			value: float64(status.Data.Server.Database.Size),
		},
	}

	if appsMetrics {
		metrics = append(metrics, []simpleMetric{
			{
				desc:  appsInstalledDesc,
				value: float64(status.Data.Nextcloud.System.Apps.Installed),
			},
			{
				desc:  appsUpdatesDesc,
				value: float64(status.Data.Nextcloud.System.Apps.AvailableUpdates),
			},
		}...)
	}

	for _, m := range metrics {
		metric, err := prometheus.NewConstMetric(m.desc, prometheus.GaugeValue, m.value)
		if err != nil {
			return fmt.Errorf("error creating metric for %s: %w", m.desc, err)
		}
		ch <- metric
	}

	return nil
}

func collectUpdate(ch chan<- prometheus.Metric, status *serverinfo.ServerInfo) error {
	systemInfo := status.Data.Nextcloud.System
	updateAvailableValue := 0.0

	// Fix small bug: its indicated as "true" even if there is no real update available.
	if systemInfo.Update.Available && systemInfo.Version != systemInfo.Update.AvailableVersion {
		updateAvailableValue = 1.0
	}

	metric, err := prometheus.NewConstMetric(systemUpdateAvailableDesc, prometheus.GaugeValue, updateAvailableValue, systemInfo.Version, systemInfo.Update.AvailableVersion)
	if err != nil {
		return fmt.Errorf("error creating metric for %s: %w", systemUpdateAvailableDesc, err)
	}
	ch <- metric

	return nil
}

func collectShares(ch chan<- prometheus.Metric, shares serverinfo.Shares) error {
	values := make(map[string]float64)
	values["user"] = float64(shares.SharesUser)
	values["group"] = float64(shares.SharesGroups)
	values["authlink"] = float64(shares.SharesLink - shares.SharesLinkNoPassword)
	values["link"] = float64(shares.SharesLink)
	values["mail"] = float64(shares.SharesMail)
	values["room"] = float64(shares.SharesRoom)

	return collectMap(ch, sharesDesc, values)
}

func collectFederatedShares(ch chan<- prometheus.Metric, shares serverinfo.Shares) error {
	values := make(map[string]float64)
	values["sent"] = float64(shares.FedSent)
	values["received"] = float64(shares.FedReceived)

	return collectMap(ch, federationsDesc, values)
}

func collectMap(ch chan<- prometheus.Metric, desc *prometheus.Desc, labelValueMap map[string]float64) error {
	for k, v := range labelValueMap {
		metric, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, v, k)
		if err != nil {
			return fmt.Errorf("error creating shares metric for %s: %w", k, err)
		}
		ch <- metric
	}

	return nil
}

func collectInfoMetric(ch chan<- prometheus.Metric, desc *prometheus.Desc, labelValues []string) error {
	metric, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, 1, labelValues...)
	if err != nil {
		return err
	}

	ch <- metric
	return nil
}
