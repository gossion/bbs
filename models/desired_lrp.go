package models

import (
	"net/url"
	"regexp"
	"time"

	"code.cloudfoundry.org/bbs/format"
)

const PreloadedRootFSScheme = "preloaded"

var processGuidPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type DesiredLRPChange struct {
	Before *DesiredLRP
	After  *DesiredLRP
}

type DesiredLRPFilter struct {
	Domain       string
	ProcessGuids []string
}

func PreloadedRootFS(stack string) string {
	return (&url.URL{
		Scheme: PreloadedRootFSScheme,
		Opaque: stack,
	}).String()
}

func NewDesiredLRP(schedInfo DesiredLRPSchedulingInfo, runInfo DesiredLRPRunInfo) DesiredLRP {
	environmentVariables := make([]*EnvironmentVariable, len(runInfo.EnvironmentVariables))
	for i := range runInfo.EnvironmentVariables {
		environmentVariables[i] = &runInfo.EnvironmentVariables[i]
	}

	egressRules := make([]*SecurityGroupRule, len(runInfo.EgressRules))
	for i := range runInfo.EgressRules {
		egressRules[i] = &runInfo.EgressRules[i]
	}

	return DesiredLRP{
		ProcessGuid:                   schedInfo.ProcessGuid,
		Domain:                        schedInfo.Domain,
		LogGuid:                       schedInfo.LogGuid,
		MemoryMb:                      schedInfo.MemoryMb,
		DiskMb:                        schedInfo.DiskMb,
		MaxPids:                       schedInfo.MaxPids,
		RootFs:                        schedInfo.RootFs,
		Instances:                     schedInfo.Instances,
		Annotation:                    schedInfo.Annotation,
		Routes:                        &schedInfo.Routes,
		ModificationTag:               &schedInfo.ModificationTag,
		EnvironmentVariables:          environmentVariables,
		CachedDependencies:            runInfo.CachedDependencies,
		Setup:                         runInfo.Setup,
		Action:                        runInfo.Action,
		Monitor:                       runInfo.Monitor,
		StartTimeoutMs:                runInfo.StartTimeoutMs,
		Privileged:                    runInfo.Privileged,
		CpuWeight:                     runInfo.CpuWeight,
		Ports:                         runInfo.Ports,
		EgressRules:                   egressRules,
		LogSource:                     runInfo.LogSource,
		MetricsGuid:                   runInfo.MetricsGuid,
		LegacyDownloadUser:            runInfo.LegacyDownloadUser,
		TrustedSystemCertificatesPath: runInfo.TrustedSystemCertificatesPath,
		VolumeMounts:                  runInfo.VolumeMounts,
		Network:                       runInfo.Network,
		PlacementTags:                 schedInfo.PlacementTags,
		CertificateProperties:         runInfo.CertificateProperties,
	}
}

func (desiredLRP *DesiredLRP) AddRunInfo(runInfo DesiredLRPRunInfo) {
	environmentVariables := make([]*EnvironmentVariable, len(runInfo.EnvironmentVariables))
	for i := range runInfo.EnvironmentVariables {
		environmentVariables[i] = &runInfo.EnvironmentVariables[i]
	}

	egressRules := make([]*SecurityGroupRule, len(runInfo.EgressRules))
	for i := range runInfo.EgressRules {
		egressRules[i] = &runInfo.EgressRules[i]
	}

	desiredLRP.EnvironmentVariables = environmentVariables
	desiredLRP.CachedDependencies = runInfo.CachedDependencies
	desiredLRP.Setup = runInfo.Setup
	desiredLRP.Action = runInfo.Action
	desiredLRP.Monitor = runInfo.Monitor
	desiredLRP.StartTimeoutMs = runInfo.StartTimeoutMs
	desiredLRP.Privileged = runInfo.Privileged
	desiredLRP.CpuWeight = runInfo.CpuWeight
	desiredLRP.Ports = runInfo.Ports
	desiredLRP.EgressRules = egressRules
	desiredLRP.LogSource = runInfo.LogSource
	desiredLRP.MetricsGuid = runInfo.MetricsGuid
	desiredLRP.LegacyDownloadUser = runInfo.LegacyDownloadUser
	desiredLRP.TrustedSystemCertificatesPath = runInfo.TrustedSystemCertificatesPath
	desiredLRP.VolumeMounts = runInfo.VolumeMounts
	desiredLRP.Network = runInfo.Network
}

func newDesiredLRPWithCachedDependenciesAsSetupActions(d *DesiredLRP) *DesiredLRP {
	d = d.Copy()
	if len(d.CachedDependencies) > 0 {

		cachedDownloads := Parallel(d.actionsFromCachedDependencies()...)

		if d.Setup != nil {
			d.Setup = WrapAction(Serial(cachedDownloads, UnwrapAction(d.Setup)))
		} else {
			d.Setup = WrapAction(Serial(cachedDownloads))
		}
		d.CachedDependencies = nil
	}

	return d
}

func (*DesiredLRP) Version() format.Version {
	return format.V2
}

func (d *DesiredLRP) VersionDownTo(v format.Version) *DesiredLRP {

	versionedLRP := d.Copy()

	switch v {

	case format.V1:
		versionedLRP.Action.SetDeprecatedTimeoutNs()
		versionedLRP.Setup.SetDeprecatedTimeoutNs()
		versionedLRP.Monitor.SetDeprecatedTimeoutNs()
		versionedLRP.DeprecatedStartTimeoutS = uint32(versionedLRP.StartTimeoutMs) / 1000
		return versionedLRP
	case format.V0:
		versionedLRP.Action.SetDeprecatedTimeoutNs()
		versionedLRP.Setup.SetDeprecatedTimeoutNs()
		versionedLRP.Monitor.SetDeprecatedTimeoutNs()
		versionedLRP.DeprecatedStartTimeoutS = uint32(versionedLRP.StartTimeoutMs) / 1000
		return newDesiredLRPWithCachedDependenciesAsSetupActions(versionedLRP)
	default:
		return versionedLRP
	}
}

func (d *DesiredLRP) DesiredLRPKey() DesiredLRPKey {
	return NewDesiredLRPKey(d.ProcessGuid, d.Domain, d.LogGuid)
}

func (d *DesiredLRP) DesiredLRPResource() DesiredLRPResource {
	return NewDesiredLRPResource(d.MemoryMb, d.DiskMb, d.MaxPids, d.RootFs)
}

func (d *DesiredLRP) DesiredLRPSchedulingInfo() DesiredLRPSchedulingInfo {
	var routes Routes
	if d.Routes != nil {
		routes = *d.Routes
	}
	var modificationTag ModificationTag
	if d.ModificationTag != nil {
		modificationTag = *d.ModificationTag
	}

	var volumePlacement VolumePlacement
	volumePlacement.DriverNames = []string{}
	for _, mount := range d.VolumeMounts {
		volumePlacement.DriverNames = append(volumePlacement.DriverNames, mount.Driver)
	}

	return NewDesiredLRPSchedulingInfo(
		d.DesiredLRPKey(),
		d.Annotation,
		d.Instances,
		d.DesiredLRPResource(),
		routes,
		modificationTag,
		&volumePlacement,
		d.PlacementTags,
	)
}

func (d *DesiredLRP) DesiredLRPRunInfo(createdAt time.Time) DesiredLRPRunInfo {
	environmentVariables := make([]EnvironmentVariable, len(d.EnvironmentVariables))
	for i := range d.EnvironmentVariables {
		environmentVariables[i] = *d.EnvironmentVariables[i]
	}

	egressRules := make([]SecurityGroupRule, len(d.EgressRules))
	for i := range d.EgressRules {
		egressRules[i] = *d.EgressRules[i]
	}
	return NewDesiredLRPRunInfo(
		d.DesiredLRPKey(),
		createdAt,
		environmentVariables,
		d.CachedDependencies,
		d.Setup,
		d.Action,
		d.Monitor,
		d.StartTimeoutMs,
		d.Privileged,
		d.CpuWeight,
		d.Ports,
		egressRules,
		d.LogSource,
		d.MetricsGuid,
		d.LegacyDownloadUser,
		d.TrustedSystemCertificatesPath,
		d.VolumeMounts,
		d.Network,
		d.CertificateProperties,
	)
}

func (d *DesiredLRP) Copy() *DesiredLRP {
	newDesired := *d
	return &newDesired
}

func (d *DesiredLRP) CreateComponents(createdAt time.Time) (DesiredLRPSchedulingInfo, DesiredLRPRunInfo) {
	return d.DesiredLRPSchedulingInfo(), d.DesiredLRPRunInfo(createdAt)
}

func (d *DesiredLRP) actionsFromCachedDependencies() []ActionInterface {
	actions := make([]ActionInterface, len(d.CachedDependencies))
	for i := range d.CachedDependencies {
		cacheDependency := d.CachedDependencies[i]
		actions[i] = &DownloadAction{
			Artifact:  cacheDependency.Name,
			From:      cacheDependency.From,
			To:        cacheDependency.To,
			CacheKey:  cacheDependency.CacheKey,
			LogSource: cacheDependency.LogSource,
			User:      d.LegacyDownloadUser,
		}
	}
	return actions
}

func (desired DesiredLRP) Validate() error {
	var validationError ValidationError

	if desired.GetDomain() == "" {
		validationError = validationError.Append(ErrInvalidField{"domain"})
	}

	if !processGuidPattern.MatchString(desired.GetProcessGuid()) {
		validationError = validationError.Append(ErrInvalidField{"process_guid"})
	}

	if desired.GetRootFs() == "" {
		validationError = validationError.Append(ErrInvalidField{"rootfs"})
	}

	rootFSURL, err := url.Parse(desired.GetRootFs())
	if err != nil || rootFSURL.Scheme == "" {
		validationError = validationError.Append(ErrInvalidField{"rootfs"})
	}

	if desired.Setup != nil {
		if err := desired.Setup.Validate(); err != nil {
			validationError = validationError.Append(ErrInvalidField{"setup"})
			validationError = validationError.Append(err)
		}
	}

	if desired.Action == nil {
		validationError = validationError.Append(ErrInvalidActionType)
	} else if err := desired.Action.Validate(); err != nil {
		validationError = validationError.Append(ErrInvalidField{"action"})
		validationError = validationError.Append(err)
	}

	if desired.Monitor != nil {
		if err := desired.Monitor.Validate(); err != nil {
			validationError = validationError.Append(ErrInvalidField{"monitor"})
			validationError = validationError.Append(err)
		}
	}

	if desired.GetInstances() < 0 {
		validationError = validationError.Append(ErrInvalidField{"instances"})
	}

	if desired.GetCpuWeight() > 100 {
		validationError = validationError.Append(ErrInvalidField{"cpu_weight"})
	}

	if desired.GetMemoryMb() < 0 {
		validationError = validationError.Append(ErrInvalidField{"memory_mb"})
	}

	if desired.GetDiskMb() < 0 {
		validationError = validationError.Append(ErrInvalidField{"disk_mb"})
	}

	if len(desired.GetAnnotation()) > maximumAnnotationLength {
		validationError = validationError.Append(ErrInvalidField{"annotation"})
	}

	if desired.GetMaxPids() < 0 {
		validationError = validationError.Append(ErrInvalidField{"max_pids"})
	}

	totalRoutesLength := 0
	if desired.Routes != nil {
		for _, value := range *desired.Routes {
			totalRoutesLength += len(*value)
			if totalRoutesLength > maximumRouteLength {
				validationError = validationError.Append(ErrInvalidField{"routes"})
				break
			}
		}
	}

	for _, rule := range desired.EgressRules {
		err := rule.Validate()
		if err != nil {
			validationError = validationError.Append(ErrInvalidField{"egress_rules"})
			validationError = validationError.Append(err)
		}
	}

	err = validateCachedDependencies(desired.CachedDependencies, desired.LegacyDownloadUser)
	if err != nil {
		validationError = validationError.Append(err)
	}

	for _, mount := range desired.VolumeMounts {
		validationError = validationError.Check(mount)
	}

	return validationError.ToError()
}

func (desired *DesiredLRPUpdate) Validate() error {
	var validationError ValidationError

	if desired.GetInstances() < 0 {
		validationError = validationError.Append(ErrInvalidField{"instances"})
	}

	if len(desired.GetAnnotation()) > maximumAnnotationLength {
		validationError = validationError.Append(ErrInvalidField{"annotation"})
	}

	totalRoutesLength := 0
	if desired.Routes != nil {
		for _, value := range *desired.Routes {
			totalRoutesLength += len(*value)
			if totalRoutesLength > maximumRouteLength {
				validationError = validationError.Append(ErrInvalidField{"routes"})
				break
			}
		}
	}

	return validationError.ToError()
}

func NewDesiredLRPKey(processGuid, domain, logGuid string) DesiredLRPKey {
	return DesiredLRPKey{
		ProcessGuid: processGuid,
		Domain:      domain,
		LogGuid:     logGuid,
	}
}

func (key DesiredLRPKey) Validate() error {
	var validationError ValidationError
	if key.GetDomain() == "" {
		validationError = validationError.Append(ErrInvalidField{"domain"})
	}

	if !processGuidPattern.MatchString(key.GetProcessGuid()) {
		validationError = validationError.Append(ErrInvalidField{"process_guid"})
	}

	return validationError.ToError()
}

func NewDesiredLRPSchedulingInfo(
	key DesiredLRPKey,
	annotation string,
	instances int32,
	resource DesiredLRPResource,
	routes Routes,
	modTag ModificationTag,
	volumePlacement *VolumePlacement,
	placementTags []string,
) DesiredLRPSchedulingInfo {
	return DesiredLRPSchedulingInfo{
		DesiredLRPKey:      key,
		Annotation:         annotation,
		Instances:          instances,
		DesiredLRPResource: resource,
		Routes:             routes,
		ModificationTag:    modTag,
		VolumePlacement:    volumePlacement,
		PlacementTags:      placementTags,
	}
}

func (s *DesiredLRPSchedulingInfo) ApplyUpdate(update *DesiredLRPUpdate) {
	if update.Instances != nil {
		s.Instances = *update.Instances
	}
	if update.Routes != nil {
		s.Routes = *update.Routes
	}
	if update.Annotation != nil {
		s.Annotation = *update.Annotation
	}
	s.ModificationTag.Increment()
}

func (*DesiredLRPSchedulingInfo) Version() format.Version {
	return format.V0
}

func (s DesiredLRPSchedulingInfo) Validate() error {
	var ve ValidationError

	ve = ve.Check(s.DesiredLRPKey, s.DesiredLRPResource, s.Routes)

	if s.GetInstances() < 0 {
		ve = ve.Append(ErrInvalidField{"instances"})
	}

	if len(s.GetAnnotation()) > maximumAnnotationLength {
		ve = ve.Append(ErrInvalidField{"annotation"})
	}

	return ve.ToError()
}

func NewDesiredLRPResource(memoryMb, diskMb, maxPids int32, rootFs string) DesiredLRPResource {
	return DesiredLRPResource{
		MemoryMb: memoryMb,
		DiskMb:   diskMb,
		MaxPids:  maxPids,
		RootFs:   rootFs,
	}
}

func (resource DesiredLRPResource) Validate() error {
	var validationError ValidationError

	rootFSURL, err := url.Parse(resource.GetRootFs())
	if err != nil || rootFSURL.Scheme == "" {
		validationError = validationError.Append(ErrInvalidField{"rootfs"})
	}

	if resource.GetMemoryMb() < 0 {
		validationError = validationError.Append(ErrInvalidField{"memory_mb"})
	}

	if resource.GetDiskMb() < 0 {
		validationError = validationError.Append(ErrInvalidField{"disk_mb"})
	}

	if resource.GetMaxPids() < 0 {
		validationError = validationError.Append(ErrInvalidField{"max_pids"})
	}

	return validationError.ToError()
}

func NewDesiredLRPRunInfo(
	key DesiredLRPKey,
	createdAt time.Time,
	envVars []EnvironmentVariable,
	cacheDeps []*CachedDependency,
	setup,
	action,
	monitor *Action,
	startTimeoutMs int64,
	privileged bool,
	cpuWeight uint32,
	ports []uint32,
	egressRules []SecurityGroupRule,
	logSource,
	metricsGuid string,
	legacyDownloadUser string,
	trustedSystemCertificatesPath string,
	volumeMounts []*VolumeMount,
	network *Network,
	certificateProperties *CertificateProperties,
) DesiredLRPRunInfo {
	return DesiredLRPRunInfo{
		DesiredLRPKey:                 key,
		CreatedAt:                     createdAt.UnixNano(),
		EnvironmentVariables:          envVars,
		CachedDependencies:            cacheDeps,
		Setup:                         setup,
		Action:                        action,
		Monitor:                       monitor,
		StartTimeoutMs:                startTimeoutMs,
		Privileged:                    privileged,
		CpuWeight:                     cpuWeight,
		Ports:                         ports,
		EgressRules:                   egressRules,
		LogSource:                     logSource,
		MetricsGuid:                   metricsGuid,
		LegacyDownloadUser:            legacyDownloadUser,
		TrustedSystemCertificatesPath: trustedSystemCertificatesPath,
		VolumeMounts:                  volumeMounts,
		Network:                       network,
		CertificateProperties:         certificateProperties,
	}
}

func (runInfo DesiredLRPRunInfo) Validate() error {
	var ve ValidationError

	ve = ve.Check(
		runInfo.DesiredLRPKey,
		runInfo.Setup,
		runInfo.Action,
		runInfo.Monitor,
	)

	for _, envVar := range runInfo.EnvironmentVariables {
		ve = ve.Check(envVar)
	}

	for _, rule := range runInfo.EgressRules {
		ve = ve.Check(rule)
	}

	if runInfo.GetCpuWeight() > 100 {
		ve = ve.Append(ErrInvalidField{"cpu_weight"})
	}

	err := validateCachedDependencies(runInfo.CachedDependencies, runInfo.LegacyDownloadUser)
	if err != nil {
		ve = ve.Append(err)
	}

	for _, mount := range runInfo.VolumeMounts {
		ve = ve.Check(mount)
	}

	return ve.ToError()
}

func (*DesiredLRPRunInfo) Version() format.Version {
	return format.V0
}

func (*CertificateProperties) Version() format.Version {
	return format.V0
}

func (CertificateProperties) Validate() error {
	return nil
}
