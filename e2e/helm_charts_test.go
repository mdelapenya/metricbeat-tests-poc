package e2e

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"strconv"

	services "github.com/elastic/metricbeat-tests-poc/cli/services"
	shell "github.com/elastic/metricbeat-tests-poc/cli/shell"

	"github.com/cucumber/godog"
	log "github.com/sirupsen/logrus"
)

var helm services.HelmManager
var kubectl services.Kubectl

func init() {
	helmVersion := "2.x"
	if value, exists := os.LookupEnv("HELM_VERSION"); exists {
		helmVersion = value
	}

	h, err := services.HelmFactory(helmVersion)
	if err != nil {
		log.Fatal(err)
	}
	helm = h
}

// HelmChartTestSuite represents a test suite for a helm chart
//nolint:unused
type HelmChartTestSuite struct {
	ClusterName       string // the name of the cluster
	KubernetesVersion string // the Kubernetes version for the test
	Name              string // the name of the chart
	Version           string // the helm chart version for the test
}

func (ts *HelmChartTestSuite) aClusterIsRunning() error {
	args := []string{"get", "clusters"}

	output, err := shell.Execute(".", "kind", args...)
	if err != nil {
		log.Fatalf("Could not check the status of the cluster. Aborting: %v", err)
	}
	if output != ts.ClusterName {
		return errors.New("The cluster is not running")
	}

	log.WithFields(log.Fields{
		"output": output,
	}).Debug("Cluster is running")
	return nil
}

func (ts *HelmChartTestSuite) addElasticRepo() {
	err := helm.AddRepo("elastic", "https://helm.elastic.co")
	if err != nil {
		log.Fatalf("Could not add Elastic Helm repo. Aborting: %v", err)
	}
}

func (ts *HelmChartTestSuite) aResourceContainsTheKey(resource string, key string) error {
	lowerResource := strings.ToLower(resource)
	escapedKey := strings.ReplaceAll(key, ".", `\.`)

	output, err := kubectl.Run("get", lowerResource, ts.getResourceName(resource), "-o", `jsonpath="{.data['` + escapedKey + `']}"`)
	if err != nil {
		return err
	}
	if output == "" {
		return errors.New("There is no " + resource + " for the " + ts.Name + " chart including " + key)
	}

	log.WithFields(log.Fields{
		"output": output,
		"name":   ts.Name,
	}).Debug("A " + resource + " resource contains the " + key + " key")

	return nil
}

func (ts *HelmChartTestSuite) aResourceManagesRBAC(resource string) error {
	lowerResource := strings.ToLower(resource)

	output, err := kubectl.Run("get", lowerResource, ts.getResourceName(resource), "-o", `jsonpath="'{.metadata.labels.chart}'"`)
	if err != nil {
		return err
	}
	if output == "" {
		return errors.New("There is no " + resource + " for the " + ts.Name + " chart")
	}

	log.WithFields(log.Fields{
		"output": output,
		"name":   ts.Name,
	}).Debug("A " + resource + " resource manages K8S RBAC")

	return nil
}

func (ts *HelmChartTestSuite) createCluster(k8sVersion string) {
	args := []string{"create", "cluster", "--name", ts.ClusterName, "--image", "kindest/node:v" + k8sVersion}

	log.Debug("Creating cluster with kind")
	output, err := shell.Execute(".", "kind", args...)
	if err != nil {
		log.Fatalf("Could not create the cluster. Aborting: %v", err)
	}
	log.WithFields(log.Fields{
		"cluster":    ts.ClusterName,
		"k8sVersion": k8sVersion,
		"output":     output,
	}).Debug("Cluster created")

	// initialise Helm after the cluster is created
	// For Helm v2.x.x we have to initialise Tiller
	// right after the k8s cluster
	err = helm.Init()
	if err != nil {
		log.Fatalf("Could not initiase Helm. Aborting: %v", err)
	}
}

func (ts *HelmChartTestSuite) deleteChart() {
	err := helm.DeleteChart(ts.Name)
	if err != nil {
		log.WithFields(log.Fields{
			"chart": ts.Name,
		}).Error("Could not delete chart")
	}
}

func (ts *HelmChartTestSuite) destroyCluster() {
	args := []string{"delete", "cluster", "--name", ts.ClusterName}

	log.Debug("Deleting cluster")
	output, err := shell.Execute(".", "kind", args...)
	if err != nil {
		log.Fatalf("Could not destroy the cluster. Aborting: %v", err)
	}
	log.WithFields(log.Fields{
		"output":  output,
		"cluster": ts.ClusterName,
	}).Debug("Cluster destroyed")
}

func (ts *HelmChartTestSuite) elasticsHelmChartIsInstalled(chart string) error {
	return ts.install(chart)
}

// getFullName returns the name plus version, in lowercase, enclosed in quotes
func (ts *HelmChartTestSuite) getFullName() string {
	return strings.ToLower("'" + ts.Name + "-" + ts.Version + "'")
}

// getKubeStateName returns the kube-state-metrics name, in lowercase, enclosed in quotes
func (ts *HelmChartTestSuite) getKubeStateMetricsName() string {
	return strings.ToLower("'" + ts.Name + "-kube-state-metrics'")
}

// getKubeStateName returns the kube-state-metrics name, in lowercase, enclosed in quotes
func (ts *HelmChartTestSuite) getResourceName(resource string) string {
	if resource == "ClusterRole" {
		return strings.ToLower(ts.Name + "-" + ts.Name + "-cluster-role")
	} else if resource == "ClusterRoleBinding" {
		return strings.ToLower(ts.Name + "-" + ts.Name + "-cluster-role-binding")
	} else if resource == "ConfigMap" {
		return strings.ToLower(ts.Name + "-" + ts.Name + "-config")
	} else if resource == "ServiceAccount" {
		return strings.ToLower(ts.Name + "-" + ts.Name)
	}

	return ""
}

func (ts *HelmChartTestSuite) install(chart string) error {
	ts.Name = chart

	elasticChart := "elastic/" + ts.Name

	flags := []string{}
	if chart == "elasticsearch" {
		// Rancher Local Path Provisioner and local-path storage class for Elasticsearch volumes
		args := []string{
			"apply", "-f", "https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml",
		}

		_, err := shell.Execute(".", "kubectl", args...)
		if err != nil {
			log.Errorf("Could not apply Rancher Local Path Provisioner: %v", err)
			return err
		}
		log.WithFields(log.Fields{
			"name": ts.Name,
		}).Debug("Rancher Local Path Provisioner and local-path storage class for Elasticsearch volumes installed")

		// workaround to use Rancher's local-path storage class for Elasticsearch volumes
		flags = []string{"--wait", "--timeout=900", "--values", "https://raw.githubusercontent.com/elastic/helm-charts/master/elasticsearch/examples/kubernetes-kind/values.yaml"}
	}

	return helm.InstallChart(ts.Name, elasticChart, ts.Version, flags)
}

func (ts *HelmChartTestSuite) installRuntimeDependencies(dependencies ...string) {
	for _, dependency := range dependencies {
		// Install Elasticsearch
		err := ts.install(dependency)
		if err != nil {
			log.Fatalf("Could not install %s as runtime dependency. Aborting: %v", dependency, err)
		}
	}
}

func (ts *HelmChartTestSuite) podsManagedByDaemonSet() error {
	output, err := kubectl.Run("get", "daemonset", "--namespace=default", "-l", "app=" + ts.Name + "-" + ts.Name, "-o", "jsonpath='{.items[0].metadata.labels.chart}'")
	if err != nil {
		return err
	}
	if output != ts.getFullName() {
		return errors.New("There is no DaemonSet for the " + ts.Name + " chart. Expected:" + ts.getFullName() + ", Actual: " + output)
	}

	log.WithFields(log.Fields{
		"output": output,
		"name":   ts.Name,
	}).Debug("A pod will be deployed on each node of the cluster by a DaemonSet")

	return nil
}

func (ts *HelmChartTestSuite) resourceWillManageAdditionalPodsForMetricsets(resource string) error {
	lowerResource := strings.ToLower(resource)

	output, err := kubectl.Run("get", lowerResource, ts.Name + "-" + ts.Name + "-metrics", "-o", "jsonpath='{.metadata.labels.chart}'")
	if err != nil {
		return err
	}
	if output != ts.getFullName() {
		return errors.New("There is no " + resource + " for the " + ts.Name + " chart. Expected:" + ts.getFullName() + ", Actual: " + output)
	}

	log.WithFields(log.Fields{
		"output": output,
		"name":   ts.Name,
	}).Debug("A " + resource + " will manage additional pods for metricsets querying internal service")

	return nil
}

func (ts *HelmChartTestSuite) volumeMountedWithNoSubpath(name string, mountPath string) error {
	return ts.volumeMountedWithSubpath(name, mountPath, "")
}

func (ts *HelmChartTestSuite) volumeMountedWithSubpath(name string, mountPath string, subPath string) error {

	getMountValues := func(key string) ([]string, error) {
		// build the arguments for capturing the volume mounts
		args := []string{
			"get", "pods", "-l", "app=" + ts.Name + "-" + ts.Name, "-o", `jsonpath="{.items[0].spec.containers[0].volumeMounts[*]['` + key + `']}"`,
		}
		output, err := shell.Execute(".", "kubectl", args...)
		if err != nil {
			return []string{}, err
		}
		output = strings.Trim(output, "\"") // remove enclosing double quotes

		return strings.Split(output, " "), nil
	}

	// get volumeMounts names
	names, err := getMountValues("name")
	if err != nil {
		return err
	}

	// Find returns the smallest index i at which x == a[i],
	// or len(a) if there is no such index.
	find := func(a []string, x string) int {
		for i, n := range a {
			if x == n {
				return i
			}
		}
		return len(a)
	}

	index := find(names, name)
	if index == len(names) {
		return fmt.Errorf("The mounted volume '%s' could not be found: %v", name, names)
	}

	// get mounts paths
	mountPaths, err := getMountValues("mountPath")
	if err != nil {
		return err
	}

	if mountPath != mountPaths[index] {
		return fmt.Errorf("The mounted volume for '%s' is not %s. Actual: %s", name, mountPath, mountPaths[index])
	}

	if subPath != "" {
		// get subpaths
		subPaths, err := getMountValues("subPath")
		if err != nil {
			return err
		}

		if subPath != subPaths[index] {
			return fmt.Errorf("The subPath for '%s' is not %s. Actual: %s", name, subPath, subPaths[index])
		}
	}

	log.WithFields(log.Fields{
		"name":      name,
		"mountPath": mountPath,
		"subPath":   subPath,
	}).Debug("The volumePath was found")

	return nil
}

func (ts *HelmChartTestSuite) willRetrieveSpecificMetrics(chartName string) error {
	kubeStateMetrics := "kube-state-metrics"

	output, err := kubectl.Run("get", "deployment", ts.Name + "-" + kubeStateMetrics, "-o", "jsonpath='{.metadata.name}'")
	if err != nil {
		return err
	}
	if output != ts.getKubeStateMetricsName() {
		return errors.New("There is no " + kubeStateMetrics + " Deployment for the " + ts.Name + " chart. Expected:" + ts.getKubeStateMetricsName() + ", Actual: " + output)
	}

	log.WithFields(log.Fields{
		"output": output,
		"name":   ts.Name,
	}).Debug("A " + kubeStateMetrics + " chart will retrieve specific Kubernetes metrics")

	return nil
}

func (ts *HelmChartTestSuite) checkResources(resourceType, selector string, min int) ([]interface{}, error) {
	resources, err := kubectl.GetResourcesBySelector(resourceType, selector)
	if err != nil {
		return nil, err
	}

	items := resources["items"].([]interface{})
	if len(items) < min {
		return nil, errors.New("Error there are not " + strconv.Itoa(min) + " " + resourceType + " for resource " +
			resourceType + "/" + ts.Name + "-" + ts.Name + " with the selector " + selector)
	}

	log.WithFields(log.Fields{
		"name":   ts.Name,
		"items": items,
	}).Debug("Checking for " + strconv.Itoa(min) + " " + resourceType + " with selector " + selector)

	return items, nil
}

func (ts *HelmChartTestSuite) aResourceWillManagePods(resourceType string) error {
	selector, err := kubectl.GetResourceSelector("deployment", ts.Name + "-" + ts.Name)
	if err != nil {
		return err
	}

	resources, err := ts.checkResources(resourceType, selector, 1)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"name":   ts.Name,
		"resources": resources,
	}).Debug("Checking the " + resourceType + " pods")

	return nil
}

func (ts *HelmChartTestSuite) aResourceWillExposePods(resourceType string) error {
	selector, err := kubectl.GetResourceSelector("deployment", ts.Name + "-" + ts.Name)
	if err != nil {
		return err
	}

	describe, err := kubectl.Describe(resourceType, selector)
	if err != nil {
		return err
	}

	endpoints := strings.SplitN(describe["Endpoints"].(string), ",", -1)
	if len(endpoints) == 0 {
		return errors.New("Error there are not Enpoints for the " + resourceType + " with the selector " + selector)
	}

	log.WithFields(log.Fields{
		"name":   ts.Name,
		"describe": describe,
	}).Debug("Checking the configmap")

	return nil
}

// HelmChartFeatureContext adds steps to the Godog test suite
//nolint:deadcode,unused
func HelmChartFeatureContext(s *godog.Suite) {
	testSuite := HelmChartTestSuite{
		ClusterName:       "helm-charts-test-suite",
		KubernetesVersion: "1.15.3",
		Version:           "7.6.1",
	}

	if value, exists := os.LookupEnv("HELM_CHART_VERSION"); exists {
		testSuite.Version = value
	}
	if value, exists := os.LookupEnv("KUBERNETES_VERSION"); exists {
		testSuite.KubernetesVersion = value
	}

	s.Step(`^a cluster is running$`, testSuite.aClusterIsRunning)
	s.Step(`^the "([^"]*)" Elastic\'s helm chart is installed$`, testSuite.elasticsHelmChartIsInstalled)
	s.Step(`^a pod will be deployed on each node of the cluster by a DaemonSet$`, testSuite.podsManagedByDaemonSet)
	s.Step(`^a "([^"]*)" will manage additional pods for metricsets querying internal services$`, testSuite.resourceWillManageAdditionalPodsForMetricsets)
	s.Step(`^a "([^"]*)" chart will retrieve specific Kubernetes metrics$`, testSuite.willRetrieveSpecificMetrics)
	s.Step(`^a "([^"]*)" resource contains the "([^"]*)" key$`, testSuite.aResourceContainsTheKey)
	s.Step(`^a "([^"]*)" resource manages RBAC$`, testSuite.aResourceManagesRBAC)
	s.Step(`^the "([^"]*)" volume is mounted at "([^"]*)" with subpath "([^"]*)"$`, testSuite.volumeMountedWithSubpath)
	s.Step(`^the "([^"]*)" volume is mounted at "([^"]*)" with no subpath$`, testSuite.volumeMountedWithNoSubpath)

	s.Step(`^a "([^"]*)" which will manage the pods$`, testSuite.aResourceWillManagePods)
	s.Step(`^a "([^"]*)" which will expose the pods as network services internal to the k8s cluster$`, testSuite.aResourceWillExposePods)

	s.BeforeSuite(func() {
		log.Debug("Before Suite...")
		toolsAreInstalled()

		testSuite.createCluster(testSuite.KubernetesVersion)
		testSuite.addElasticRepo()
		testSuite.installRuntimeDependencies("elasticsearch")
	})
	s.BeforeScenario(func(interface{}) {
		log.Info("Before Helm scenario...")
	})
	s.AfterSuite(func() {
		log.Debug("After Suite...")
		testSuite.destroyCluster()
	})
	s.AfterScenario(func(interface{}, error) {
		log.Debug("After Helm scenario...")
		testSuite.deleteChart()
	})
}

//nolint:unused
func toolsAreInstalled() {
	binaries := []string{
		"kind",
		"kubectl",
		"helm",
	}

	shell.CheckInstalledSoftware(binaries)
}
