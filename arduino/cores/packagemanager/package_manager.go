// This file is part of arduino-cli.
//
// Copyright 2020 ARDUINO SA (http://www.arduino.cc/)
//
// This software is released under the GNU General Public License version 3,
// which covers the main part of arduino-cli.
// The terms of this license can be found at:
// https://www.gnu.org/licenses/gpl-3.0.en.html
//
// You can be released from the requirements of the above licenses by purchasing
// a commercial license. Buying such a license is mandatory if you want to
// modify or otherwise use the software for commercial activities involving the
// Arduino software without disclosing the source code of your own applications.
// To purchase a commercial license, send an email to license@arduino.cc.

package packagemanager

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/arduino/arduino-cli/arduino/cores"
	"github.com/arduino/arduino-cli/arduino/cores/packageindex"
	"github.com/arduino/arduino-cli/arduino/discovery/discoverymanager"
	"github.com/arduino/arduino-cli/arduino/sketch"
	"github.com/arduino/arduino-cli/i18n"
	paths "github.com/arduino/go-paths-helper"
	properties "github.com/arduino/go-properties-orderedmap"
	"github.com/sirupsen/logrus"
	semver "go.bug.st/relaxed-semver"
)

// PackageManager defines the superior oracle which understands all about
// Arduino Packages, how to parse them, download, and so on.
//
// The manager also keeps track of the status of the Packages (their Platform Releases, actually)
// installed in the system.
type PackageManager struct {
	packagesLock                   sync.RWMutex // Protects packages and packagesCustomGlobalProperties
	packages                       cores.Packages
	packagesCustomGlobalProperties *properties.Map

	log              logrus.FieldLogger
	IndexDir         *paths.Path
	PackagesDir      *paths.Path
	DownloadDir      *paths.Path
	tempDir          *paths.Path
	profile          *sketch.Profile
	discoveryManager *discoverymanager.DiscoveryManager
	userAgent        string
}

// Builder is used to create a new PackageManager. The builder
// has methods to load platforms and tools to actually build the PackageManager.
// Once the PackageManager is built, it cannot be changed anymore.
type Builder PackageManager

// Explorer is used to query the PackageManager. When used it holds
// a read-only lock on the PackageManager that must be released when the
// job is completed.
type Explorer PackageManager

var tr = i18n.Tr

// NewBuilder returns a new Builder
func NewBuilder(indexDir, packagesDir, downloadDir, tempDir *paths.Path, userAgent string) *Builder {
	return &Builder{
		log:                            logrus.StandardLogger(),
		packages:                       cores.NewPackages(),
		IndexDir:                       indexDir,
		PackagesDir:                    packagesDir,
		DownloadDir:                    downloadDir,
		tempDir:                        tempDir,
		packagesCustomGlobalProperties: properties.NewMap(),
		discoveryManager:               discoverymanager.New(),
		userAgent:                      userAgent,
	}
}

// BuildIntoExistingPackageManager will overwrite the given PackageManager instead
// of building a new one.
func (pmb *Builder) BuildIntoExistingPackageManager(target *PackageManager) {
	target.packagesLock.Lock()
	defer target.packagesLock.Unlock()
	target.log = pmb.log
	target.packages = pmb.packages
	target.IndexDir = pmb.IndexDir
	target.PackagesDir = pmb.PackagesDir
	target.DownloadDir = pmb.DownloadDir
	target.tempDir = pmb.tempDir
	target.packagesCustomGlobalProperties = pmb.packagesCustomGlobalProperties
	target.profile = pmb.profile
	target.discoveryManager = pmb.discoveryManager
	target.userAgent = pmb.userAgent
}

// Build builds a new PackageManager.
func (pmb *Builder) Build() *PackageManager {
	res := &PackageManager{}
	pmb.BuildIntoExistingPackageManager(res)
	return res
}

// NewBuilder creates a Builder with the same configuration
// of this PackageManager. A "commit" function callback is returned: calling
// this function will make the builder write the new configuration into this
// PackageManager.
func (pm *PackageManager) NewBuilder() (builder *Builder, commit func()) {
	pmb := NewBuilder(pm.IndexDir, pm.PackagesDir, pm.DownloadDir, pm.tempDir, pm.userAgent)
	return pmb, func() {
		pmb.BuildIntoExistingPackageManager(pm)
	}
}

// NewExplorer creates an Explorer for this PackageManager.
// The Explorer will keep a read-lock on the underlying PackageManager,
// the user must call the "release" callback function to release the lock
// when the Explorer is no more needed.
func (pm *PackageManager) NewExplorer() (explorer *Explorer, release func()) {
	pm.packagesLock.RLock()
	return &Explorer{
		log:                            pm.log,
		packages:                       pm.packages,
		IndexDir:                       pm.IndexDir,
		PackagesDir:                    pm.PackagesDir,
		DownloadDir:                    pm.DownloadDir,
		tempDir:                        pm.tempDir,
		packagesCustomGlobalProperties: pm.packagesCustomGlobalProperties,
		profile:                        pm.profile,
		discoveryManager:               pm.discoveryManager,
		userAgent:                      pm.userAgent,
	}, pm.packagesLock.RUnlock
}

// GetProfile returns the active profile for this package manager, or nil if no profile is selected.
func (pme *Explorer) GetProfile() *sketch.Profile {
	return pme.profile
}

// GetEnvVarsForSpawnedProcess produces a set of environment variables that
// must be sent to all processes spawned from the arduino-cli.
func (pme *Explorer) GetEnvVarsForSpawnedProcess() []string {
	if pme == nil {
		return nil
	}
	return []string{
		"ARDUINO_USER_AGENT=" + pme.userAgent,
	}
}

// DiscoveryManager returns the DiscoveryManager in use by this PackageManager
func (pme *Explorer) DiscoveryManager() *discoverymanager.DiscoveryManager {
	return pme.discoveryManager
}

// GetOrCreatePackage returns the specified Package or creates an empty one
// filling all the cross-references
func (pmb *Builder) GetOrCreatePackage(packager string) *cores.Package {
	return pmb.packages.GetOrCreatePackage(packager)
}

// GetPackages returns the internal packages structure for direct usage.
// Deprecated: do not access packages directly, but use specific Explorer methods when possible.
func (pme *Explorer) GetPackages() cores.Packages {
	return pme.packages
}

// GetCustomGlobalProperties returns the user defined custom global
// properties for installed platforms.
func (pme *Explorer) GetCustomGlobalProperties() *properties.Map {
	return pme.packagesCustomGlobalProperties
}

// FindPlatformReleaseProvidingBoardsWithVidPid FIXMEDOC
func (pme *Explorer) FindPlatformReleaseProvidingBoardsWithVidPid(vid, pid string) []*cores.PlatformRelease {
	res := []*cores.PlatformRelease{}
	for _, targetPackage := range pme.packages {
		for _, targetPlatform := range targetPackage.Platforms {
			platformRelease := targetPlatform.GetLatestRelease()
			if platformRelease == nil {
				continue
			}
			for _, boardManifest := range platformRelease.BoardsManifest {
				if boardManifest.HasUsbID(vid, pid) {
					res = append(res, platformRelease)
					break
				}
			}
		}
	}
	return res
}

// FindBoardsWithVidPid FIXMEDOC
func (pme *Explorer) FindBoardsWithVidPid(vid, pid string) []*cores.Board {
	res := []*cores.Board{}
	for _, targetPackage := range pme.packages {
		for _, targetPlatform := range targetPackage.Platforms {
			if platform := pme.GetInstalledPlatformRelease(targetPlatform); platform != nil {
				for _, board := range platform.Boards {
					if board.HasUsbID(vid, pid) {
						res = append(res, board)
					}
				}
			}
		}
	}
	return res
}

// FindBoardsWithID FIXMEDOC
func (pme *Explorer) FindBoardsWithID(id string) []*cores.Board {
	res := []*cores.Board{}
	for _, targetPackage := range pme.packages {
		for _, targetPlatform := range targetPackage.Platforms {
			if platform := pme.GetInstalledPlatformRelease(targetPlatform); platform != nil {
				for _, board := range platform.Boards {
					if board.BoardID == id {
						res = append(res, board)
					}
				}
			}
		}
	}
	return res
}

// FindBoardWithFQBN returns the board identified by the fqbn, or an error
func (pme *Explorer) FindBoardWithFQBN(fqbnIn string) (*cores.Board, error) {
	fqbn, err := cores.ParseFQBN(fqbnIn)
	if err != nil {
		return nil, fmt.Errorf(tr("parsing fqbn: %s"), err)
	}

	_, _, board, _, _, err := pme.ResolveFQBN(fqbn)
	return board, err
}

// ResolveFQBN returns, in order:
//
// - the Package pointed by the fqbn
//
// - the PlatformRelease pointed by the fqbn
//
// - the Board pointed by the fqbn
//
// - the build properties for the board considering also the
// configuration part of the fqbn
//
// - the PlatformRelease to be used for the build (if the board
// requires a 3rd party core it may be different from the
// PlatformRelease pointed by the fqbn)
//
// - an error if any of the above is not found
//
// In case of error the partial results found in the meantime are
// returned together with the error.
func (pme *Explorer) ResolveFQBN(fqbn *cores.FQBN) (
	*cores.Package, *cores.PlatformRelease, *cores.Board,
	*properties.Map, *cores.PlatformRelease, error) {

	// Find package
	targetPackage := pme.packages[fqbn.Package]
	if targetPackage == nil {
		return nil, nil, nil, nil, nil,
			fmt.Errorf(tr("unknown package %s"), fqbn.Package)
	}

	// Find platform
	platform := targetPackage.Platforms[fqbn.PlatformArch]
	if platform == nil {
		return targetPackage, nil, nil, nil, nil,
			fmt.Errorf(tr("unknown platform %s:%s"), targetPackage, fqbn.PlatformArch)
	}
	platformRelease := pme.GetInstalledPlatformRelease(platform)
	if platformRelease == nil {
		return targetPackage, nil, nil, nil, nil,
			fmt.Errorf(tr("platform %s is not installed"), platform)
	}

	// Find board
	board := platformRelease.Boards[fqbn.BoardID]
	if board == nil {
		return targetPackage, platformRelease, nil, nil, nil,
			fmt.Errorf(tr("board %s not found"), fqbn.StringWithoutConfig())
	}

	buildProperties, err := board.GetBuildProperties(fqbn.Configs)
	if err != nil {
		return targetPackage, platformRelease, board, nil, nil,
			fmt.Errorf(tr("getting build properties for board %[1]s: %[2]s"), board, err)
	}

	// Determine the platform used for the build (in case the board refers
	// to a core contained in another platform)
	buildPlatformRelease := platformRelease
	coreParts := strings.Split(buildProperties.Get("build.core"), ":")
	if len(coreParts) > 1 {
		referredPackage := coreParts[0]
		buildPackage := pme.packages[referredPackage]
		if buildPackage == nil {
			return targetPackage, platformRelease, board, buildProperties, nil,
				fmt.Errorf(tr("missing package %[1]s referenced by board %[2]s"), referredPackage, fqbn)
		}
		buildPlatform := buildPackage.Platforms[fqbn.PlatformArch]
		if buildPlatform == nil {
			return targetPackage, platformRelease, board, buildProperties, nil,
				fmt.Errorf(tr("missing platform %[1]s:%[2]s referenced by board %[3]s"), referredPackage, fqbn.PlatformArch, fqbn)
		}
		buildPlatformRelease = pme.GetInstalledPlatformRelease(buildPlatform)
		if buildPlatformRelease == nil {
			return targetPackage, platformRelease, board, buildProperties, nil,
				fmt.Errorf(tr("missing platform release %[1]s:%[2]s referenced by board %[3]s"), referredPackage, fqbn.PlatformArch, fqbn)
		}
	}

	// No errors... phew!
	return targetPackage, platformRelease, board, buildProperties, buildPlatformRelease, nil
}

// LoadPackageIndex loads a package index by looking up the local cached file from the specified URL
func (pmb *Builder) LoadPackageIndex(URL *url.URL) error {
	indexFileName := path.Base(URL.Path)
	if strings.HasSuffix(indexFileName, ".tar.bz2") {
		indexFileName = strings.TrimSuffix(indexFileName, ".tar.bz2") + ".json"
	}
	indexPath := pmb.IndexDir.Join(indexFileName)
	index, err := packageindex.LoadIndex(indexPath)
	if err != nil {
		return fmt.Errorf(tr("loading json index file %[1]s: %[2]s"), indexPath, err)
	}

	for _, p := range index.Packages {
		p.URL = URL.String()
	}

	index.MergeIntoPackages(pmb.packages)
	return nil
}

// LoadPackageIndexFromFile load a package index from the specified file
func (pmb *Builder) LoadPackageIndexFromFile(indexPath *paths.Path) (*packageindex.Index, error) {
	index, err := packageindex.LoadIndex(indexPath)
	if err != nil {
		return nil, fmt.Errorf(tr("loading json index file %[1]s: %[2]s"), indexPath, err)
	}

	index.MergeIntoPackages(pmb.packages)
	return index, nil
}

// Package looks for the Package with the given name, returning a structure
// able to perform further operations on that given resource
func (pme *Explorer) Package(name string) *PackageActions {
	//TODO: perhaps these 2 structure should be merged? cores.Packages vs pkgmgr??
	var err error
	thePackage := pme.packages[name]
	if thePackage == nil {
		err = fmt.Errorf(tr("package '%s' not found"), name)
	}
	return &PackageActions{
		aPackage:     thePackage,
		forwardError: err,
	}
}

// Actions that can be done on a Package

// PackageActions defines what actions can be performed on the specific Package
// It serves as a status container for the fluent APIs
type PackageActions struct {
	aPackage     *cores.Package
	forwardError error
}

// Tool looks for the Tool with the given name, returning a structure
// able to perform further operations on that given resource
func (pa *PackageActions) Tool(name string) *ToolActions {
	var tool *cores.Tool
	err := pa.forwardError
	if err == nil {
		tool = pa.aPackage.Tools[name]

		if tool == nil {
			err = fmt.Errorf(tr("tool '%[1]s' not found in package '%[2]s'"), name, pa.aPackage.Name)
		}
	}
	return &ToolActions{
		tool:         tool,
		forwardError: err,
	}
}

// END -- Actions that can be done on a Package

// Actions that can be done on a Tool

// ToolActions defines what actions can be performed on the specific Tool
// It serves as a status container for the fluent APIs
type ToolActions struct {
	tool         *cores.Tool
	forwardError error
}

// Get returns the final representation of the Tool
func (ta *ToolActions) Get() (*cores.Tool, error) {
	err := ta.forwardError
	if err == nil {
		return ta.tool, nil
	}
	return nil, err
}

// IsInstalled checks whether any release of the Tool is installed in the system
func (ta *ToolActions) IsInstalled() (bool, error) {
	if ta.forwardError != nil {
		return false, ta.forwardError
	}

	for _, release := range ta.tool.Releases {
		if release.IsInstalled() {
			return true, nil
		}
	}
	return false, nil
}

// Release FIXMEDOC
func (ta *ToolActions) Release(version *semver.RelaxedVersion) *ToolReleaseActions {
	if ta.forwardError != nil {
		return &ToolReleaseActions{forwardError: ta.forwardError}
	}
	release := ta.tool.FindReleaseWithRelaxedVersion(version)
	if release == nil {
		return &ToolReleaseActions{forwardError: fmt.Errorf(tr("release %[1]s not found for tool %[2]s"), version, ta.tool.String())}
	}
	return &ToolReleaseActions{release: release}
}

// END -- Actions that can be done on a Tool

// ToolReleaseActions defines what actions can be performed on the specific ToolRelease
// It serves as a status container for the fluent APIs
type ToolReleaseActions struct {
	release      *cores.ToolRelease
	forwardError error
}

// Get FIXMEDOC
func (tr *ToolReleaseActions) Get() (*cores.ToolRelease, error) {
	if tr.forwardError != nil {
		return nil, tr.forwardError
	}
	return tr.release, nil
}

// GetInstalledPlatformRelease returns the PlatformRelease installed (it is chosen)
func (pme *Explorer) GetInstalledPlatformRelease(platform *cores.Platform) *cores.PlatformRelease {
	releases := platform.GetAllInstalled()
	if len(releases) == 0 {
		return nil
	}

	debug := func(msg string, pl *cores.PlatformRelease) {
		pme.log.WithField("bundle", pl.IsIDEBundled).
			WithField("version", pl.Version).
			WithField("managed", pme.IsManagedPlatformRelease(pl)).
			Debugf("%s: %s", msg, pl)
	}

	best := releases[0]
	bestIsManaged := pme.IsManagedPlatformRelease(best)
	debug("current best", best)

	for _, candidate := range releases[1:] {
		candidateIsManaged := pme.IsManagedPlatformRelease(candidate)
		debug("candidate", candidate)
		// TODO: Disentangle this algorithm and make it more straightforward
		if bestIsManaged == candidateIsManaged {
			if best.IsIDEBundled == candidate.IsIDEBundled {
				if candidate.Version.GreaterThan(best.Version) {
					best = candidate
				}
			}
			if best.IsIDEBundled && !candidate.IsIDEBundled {
				best = candidate
			}
		}
		if !bestIsManaged && candidateIsManaged {
			best = candidate
			bestIsManaged = true
		}
		debug("current best", best)
	}
	return best
}

// GetAllInstalledToolsReleases FIXMEDOC
func (pme *Explorer) GetAllInstalledToolsReleases() []*cores.ToolRelease {
	tools := []*cores.ToolRelease{}
	for _, targetPackage := range pme.packages {
		for _, tool := range targetPackage.Tools {
			for _, release := range tool.Releases {
				if release.IsInstalled() {
					tools = append(tools, release)
				}
			}
		}
	}
	return tools
}

// InstalledPlatformReleases returns all installed PlatformReleases. This function is
// useful to range all PlatformReleases in for loops.
func (pme *Explorer) InstalledPlatformReleases() []*cores.PlatformRelease {
	platforms := []*cores.PlatformRelease{}
	for _, targetPackage := range pme.packages {
		for _, platform := range targetPackage.Platforms {
			platforms = append(platforms, platform.GetAllInstalled()...)
		}
	}
	return platforms
}

// InstalledBoards returns all installed Boards. This function is useful to range
// all Boards in for loops.
func (pme *Explorer) InstalledBoards() []*cores.Board {
	boards := []*cores.Board{}
	for _, targetPackage := range pme.packages {
		for _, platform := range targetPackage.Platforms {
			for _, release := range platform.GetAllInstalled() {
				for _, board := range release.Boards {
					boards = append(boards, board)
				}
			}
		}
	}
	return boards
}

// FindToolsRequiredFromPlatformRelease returns a list of ToolReleases needed by the specified PlatformRelease.
// If a ToolRelease is not found return an error
func (pme *Explorer) FindToolsRequiredFromPlatformRelease(platform *cores.PlatformRelease) ([]*cores.ToolRelease, error) {
	pme.log.Infof("Searching tools required for platform %s", platform)

	// maps "PACKAGER:TOOL" => ToolRelease
	foundTools := map[string]*cores.ToolRelease{}
	// A Platform may not specify required tools (because it's a platform that comes from a
	// user/hardware dir without a package_index.json) then add all available tools
	for _, targetPackage := range pme.packages {
		for _, tool := range targetPackage.Tools {
			rel := tool.GetLatestInstalled()
			if rel != nil {
				foundTools[rel.Tool.Name] = rel
			}
		}
	}
	// replace the default tools above with the specific required by the current platform
	requiredTools := []*cores.ToolRelease{}
	platform.ToolDependencies.Sort()
	for _, toolDep := range platform.ToolDependencies {
		pme.log.WithField("tool", toolDep).Infof("Required tool")
		tool := pme.FindToolDependency(toolDep)
		if tool == nil {
			return nil, fmt.Errorf(tr("tool release not found: %s"), toolDep)
		}
		requiredTools = append(requiredTools, tool)
		delete(foundTools, tool.Tool.Name)
	}

	platform.DiscoveryDependencies.Sort()
	for _, discoveryDep := range platform.DiscoveryDependencies {
		pme.log.WithField("discovery", discoveryDep).Infof("Required discovery")
		tool := pme.FindDiscoveryDependency(discoveryDep)
		if tool == nil {
			return nil, fmt.Errorf(tr("discovery release not found: %s"), discoveryDep)
		}
		requiredTools = append(requiredTools, tool)
		delete(foundTools, tool.Tool.Name)
	}

	platform.MonitorDependencies.Sort()
	for _, monitorDep := range platform.MonitorDependencies {
		pme.log.WithField("monitor", monitorDep).Infof("Required monitor")
		tool := pme.FindMonitorDependency(monitorDep)
		if tool == nil {
			return nil, fmt.Errorf(tr("monitor release not found: %s"), monitorDep)
		}
		requiredTools = append(requiredTools, tool)
		delete(foundTools, tool.Tool.Name)
	}

	for _, toolRel := range foundTools {
		requiredTools = append(requiredTools, toolRel)
	}
	return requiredTools, nil
}

// GetTool searches for tool in all packages and platforms.
func (pme *Explorer) GetTool(toolID string) *cores.Tool {
	split := strings.Split(toolID, ":")
	if len(split) != 2 {
		return nil
	}
	if pack, ok := pme.packages[split[0]]; !ok {
		return nil
	} else if tool, ok := pack.Tools[split[1]]; !ok {
		return nil
	} else {
		return tool
	}
}

// FindToolsRequiredForBoard FIXMEDOC
func (pme *Explorer) FindToolsRequiredForBoard(board *cores.Board) ([]*cores.ToolRelease, error) {
	pme.log.Infof("Searching tools required for board %s", board)

	// core := board.Properties["build.core"]
	platform := board.PlatformRelease

	// maps "PACKAGER:TOOL" => ToolRelease
	foundTools := map[string]*cores.ToolRelease{}

	// a Platform may not specify required tools (because it's a platform that comes from a
	// user/hardware dir without a package_index.json) then add all available tools
	for _, targetPackage := range pme.packages {
		for _, tool := range targetPackage.Tools {
			rel := tool.GetLatestInstalled()
			if rel != nil {
				foundTools[rel.Tool.Name] = rel
			}
		}
	}

	// replace the default tools above with the specific required by the current platform
	requiredTools := []*cores.ToolRelease{}
	platform.ToolDependencies.Sort()
	for _, toolDep := range platform.ToolDependencies {
		pme.log.WithField("tool", toolDep).Infof("Required tool")
		tool := pme.FindToolDependency(toolDep)
		if tool == nil {
			return nil, fmt.Errorf(tr("tool release not found: %s"), toolDep)
		}
		requiredTools = append(requiredTools, tool)
		delete(foundTools, tool.Tool.Name)
	}

	for _, toolRel := range foundTools {
		requiredTools = append(requiredTools, toolRel)
	}
	return requiredTools, nil
}

// FindToolDependency returns the ToolRelease referenced by the ToolDependency or nil if
// the referenced tool doesn't exists.
func (pme *Explorer) FindToolDependency(dep *cores.ToolDependency) *cores.ToolRelease {
	toolRelease, err := pme.Package(dep.ToolPackager).Tool(dep.ToolName).Release(dep.ToolVersion).Get()
	if err != nil {
		return nil
	}
	return toolRelease
}

// FindDiscoveryDependency returns the ToolRelease referenced by the DiscoveryDepenency or nil if
// the referenced discovery doesn't exists.
func (pme *Explorer) FindDiscoveryDependency(discovery *cores.DiscoveryDependency) *cores.ToolRelease {
	if pack := pme.packages[discovery.Packager]; pack == nil {
		return nil
	} else if toolRelease := pack.Tools[discovery.Name]; toolRelease == nil {
		return nil
	} else {
		return toolRelease.GetLatestInstalled()
	}
}

// FindMonitorDependency returns the ToolRelease referenced by the MonitorDepenency or nil if
// the referenced monitor doesn't exists.
func (pme *Explorer) FindMonitorDependency(discovery *cores.MonitorDependency) *cores.ToolRelease {
	if pack := pme.packages[discovery.Packager]; pack == nil {
		return nil
	} else if toolRelease := pack.Tools[discovery.Name]; toolRelease == nil {
		return nil
	} else {
		return toolRelease.GetLatestInstalled()
	}
}
