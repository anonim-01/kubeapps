// Copyright 2021-2023 the Kubeapps contributors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/plugins/pkg/k8sutils"
	authorizationv1 "k8s.io/api/authorization/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	k8stesting "k8s.io/client-go/testing"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/cppforlife/go-cli-ui/ui"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kappctrlv1alpha1 "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apis/kappctrl/v1alpha1"
	packagingv1alpha1 "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apis/packaging/v1alpha1"
	datapackagingv1alpha1 "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/apis/datapackaging/v1alpha1"
	kappctrlpackageinstall "github.com/vmware-tanzu/carvel-kapp-controller/pkg/packageinstall"
	ctlapp "github.com/vmware-tanzu/carvel-kapp/pkg/kapp/app"
	kappcmdapp "github.com/vmware-tanzu/carvel-kapp/pkg/kapp/cmd/app"
	kappcmdcore "github.com/vmware-tanzu/carvel-kapp/pkg/kapp/cmd/core"
	kappcmdtools "github.com/vmware-tanzu/carvel-kapp/pkg/kapp/cmd/tools"
	"github.com/vmware-tanzu/carvel-kapp/pkg/kapp/logger"
	ctlres "github.com/vmware-tanzu/carvel-kapp/pkg/kapp/resources"
	vendirversions "github.com/vmware-tanzu/carvel-vendir/pkg/vendir/versions/v1alpha1"
	corev1 "github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/gen/core/packages/v1alpha1"
	pluginv1 "github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/gen/core/plugins/v1alpha1"
	kappcorev1 "github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/gen/plugins/kapp_controller/packages/v1alpha1"
	"github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/plugins/pkg/clientgetter"
	"github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/plugins/pkg/pkgutils"
	k8scorev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	disfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	typfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

var ignoreUnexported = cmpopts.IgnoreUnexported(
	anypb.Any{},
	corev1.AvailablePackageDetail{},
	corev1.AvailablePackageReference{},
	corev1.AvailablePackageSummary{},
	corev1.Context{},
	corev1.CreateInstalledPackageResponse{},
	corev1.CreateInstalledPackageResponse{},
	corev1.DeleteInstalledPackageResponse{},
	corev1.GetAvailablePackageVersionsResponse{},
	corev1.GetAvailablePackageVersionsResponse{},
	corev1.GetInstalledPackageResourceRefsResponse{},
	corev1.GetInstalledPackageResourceRefsResponse{},
	corev1.GetPackageRepositoryDetailResponse{},
	corev1.InstalledPackageDetail{},
	corev1.InstalledPackageReference{},
	corev1.InstalledPackageStatus{},
	corev1.InstalledPackageSummary{},
	corev1.Maintainer{},
	corev1.PackageAppVersion{},
	corev1.PackageRepositoryAuth{},
	corev1.PackageRepositoryAuth_DockerCreds{},
	corev1.PackageRepositoryAuth_Header{},
	corev1.PackageRepositoryAuth_SecretRef{},
	corev1.PackageRepositoryAuth_SshCreds{},
	corev1.PackageRepositoryAuth_TlsCertKey{},
	corev1.PackageRepositoryAuth_UsernamePassword{},
	corev1.PackageRepositoryDetail{},
	corev1.PackageRepositoryReference{},
	corev1.PackageRepositoryStatus{},
	corev1.GetPackageRepositorySummariesResponse{},
	corev1.PackageRepositorySummary{},
	corev1.ReconciliationOptions{},
	corev1.ResourceRef{},
	corev1.DockerCredentials{},
	corev1.SecretKeyReference{},
	corev1.SshCredentials{},
	corev1.TlsCertKey{},
	corev1.UsernamePassword{},
	corev1.UpdateInstalledPackageResponse{},
	corev1.VersionReference{},
	kappControllerPluginParsedConfig{},
	pluginv1.Plugin{},
	corev1.GetPackageRepositoryPermissionsResponse{},
	corev1.PackageRepositoriesPermissions{},
)

const demoGlobalPackagingNamespace = "kapp-controller-packaging-global"

var defaultContext = &corev1.Context{Cluster: "default", Namespace: "default"}
var defaultGlobalContext = &corev1.Context{Cluster: defaultContext.Cluster, Namespace: demoGlobalPackagingNamespace}

var defaultTypeMeta = metav1.TypeMeta{
	Kind:       pkgRepositoryResource,
	APIVersion: packagingAPIVersion,
}

var datapackagingAPIVersion = fmt.Sprintf("%s/%s", datapackagingv1alpha1.SchemeGroupVersion.Group, datapackagingv1alpha1.SchemeGroupVersion.Version)
var packagingAPIVersion = fmt.Sprintf("%s/%s", packagingv1alpha1.SchemeGroupVersion.Group, packagingv1alpha1.SchemeGroupVersion.Version)
var kappctrlAPIVersion = fmt.Sprintf("%s/%s", kappctrlv1alpha1.SchemeGroupVersion.Group, kappctrlv1alpha1.SchemeGroupVersion.Version)

// available packages
func TestGetAvailablePackageSummaries(t *testing.T) {
	testCases := []struct {
		name              string
		existingObjects   []k8sruntime.Object
		expectedPackages  []*corev1.AvailablePackageSummary
		paginationOptions corev1.PaginationOptions
		filterOptions     corev1.FilterOptions
		expectedErrorCode connect.Code
	}{
		{
			name:             "it returns without error if there are no packages available",
			expectedPackages: []*corev1.AvailablePackageSummary{},
		},
		{
			name: "it returns an internal error status if there is no corresponding package for a package metadata",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
			},
			expectedErrorCode: connect.CodeInternal,
		},
		{
			name: "it returns an invalid argument error status if a page is requested that doesn't exist",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
			},
			paginationOptions: corev1.PaginationOptions{
				PageToken: "2",
				PageSize:  1,
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "it returns carvel package summaries with basic info from the cluster",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Tombi!",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "An awesome game from the 90's",
						LongDescription:    "Tombi! is an open world platform-adventure game with RPG elements.",
						Categories:         []string{"platforms", "rpg"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tombi!",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com.1.2.5",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tombi.foo.example.com",
						Version:                         "1.2.5",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1997, time.December, 25, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tetris.foo.example.com",
					},
					Name:        "tetris.foo.example.com",
					DisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					Categories:       []string{"logging", "daemon-set"},
				},
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tombi.foo.example.com",
					},
					Name:        "tombi.foo.example.com",
					DisplayName: "Tombi!",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.5",
						AppVersion: "1.2.5",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "An awesome game from the 90's",
					Categories:       []string{"platforms", "rpg"},
				},
			},
		},
		// The TAP 1.0.2 repository had a pkg for contour without any
		// corresponding pkgmeta. Let's handle this gracefully.
		{
			name: "it returns carvel package summaries with basic info from the cluster even when there's a missing pkg meta",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Tombi!",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "An awesome game from the 90's",
						LongDescription:    "Tombi! is an open world platform-adventure game with RPG elements.",
						Categories:         []string{"platforms", "rpg"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tombi!",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tinkle.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tinkle.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com.1.2.5",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tombi.foo.example.com",
						Version:                         "1.2.5",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1997, time.December, 25, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tetris.foo.example.com",
					},
					Name:        "tetris.foo.example.com",
					DisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					Categories:       []string{"logging", "daemon-set"},
				},
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tombi.foo.example.com",
					},
					Name:        "tombi.foo.example.com",
					DisplayName: "Tombi!",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.5",
						AppVersion: "1.2.5",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "An awesome game from the 90's",
					Categories:       []string{"platforms", "rpg"},
				},
			},
		},
		{
			name: "it returns carvel package summaries with complete metadata",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tetris.foo.example.com",
					},
					Name:        "tetris.foo.example.com",
					DisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					Categories:       []string{"logging", "daemon-set"},
				},
			},
		},
		{
			name: "it returns carvel package summaries with repo-based identifiers",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
						Annotations: map[string]string{
							REPO_REF_ANNOTATION: "default/tce-repo",
						},
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "tce-repo/tetris.foo.example.com",
					},
					Name:        "tetris.foo.example.com",
					DisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					Categories:       []string{"logging", "daemon-set"},
				},
			},
		},
		{
			name: "it returns the latest semver version in the latest version field without relying on default alpha sorting",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.10",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.10",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.4",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.4",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tetris.foo.example.com",
					},
					Name:        "tetris.foo.example.com",
					DisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.10",
						AppVersion: "1.2.10",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					Categories:       []string{"logging", "daemon-set"},
				},
			},
		},
		{
			name: "it returns paginated carvel package summaries with an item offset (not a page offset)",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Tombi!",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "An awesome game from the 90's",
						LongDescription:    "Tombi! is an open world platform-adventure game with RPG elements.",
						Categories:         []string{"platforms", "rpg"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tombi!",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tunotherone.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Tunotherone!",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "Another awesome game from the 90's",
						LongDescription:    "Tunotherone! is another open world platform-adventure game with RPG elements.",
						Categories:         []string{"platforms", "rpg"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "tunotherone",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com.1.2.5",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tombi.foo.example.com",
						Version:                         "1.2.5",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1997, time.December, 25, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tunotherone.foo.example.com.3.2.5",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tunotherone.foo.example.com",
						Version:                         "3.2.5",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1997, time.December, 25, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			paginationOptions: corev1.PaginationOptions{
				PageToken: "1",
				PageSize:  2,
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tombi.foo.example.com",
					},
					Name:        "tombi.foo.example.com",
					DisplayName: "Tombi!",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.5",
						AppVersion: "1.2.5",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "An awesome game from the 90's",
					Categories:       []string{"platforms", "rpg"},
				},
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tunotherone.foo.example.com",
					},
					Name:        "tunotherone.foo.example.com",
					DisplayName: "Tunotherone!",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "3.2.5",
						AppVersion: "3.2.5",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "Another awesome game from the 90's",
					Categories:       []string{"platforms", "rpg"},
				},
			},
		},
		{
			name: "it returns paginated carvel package summaries limited to the page size",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Tombi!",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "An awesome game from the 90's",
						LongDescription:    "Tombi! is an open world platform-adventure game with RPG elements.",
						Categories:         []string{"platforms", "rpg"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tombi!",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com.1.2.5",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tombi.foo.example.com",
						Version:                         "1.2.5",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1997, time.December, 25, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			paginationOptions: corev1.PaginationOptions{
				PageToken: "0",
				PageSize:  1,
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tetris.foo.example.com",
					},
					Name:        "tetris.foo.example.com",
					DisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					Categories:       []string{"logging", "daemon-set"},
				},
			},
		},
		{
			name: "it returns carvel package summaries filtered by a query",
			filterOptions: corev1.FilterOptions{
				Query: "tetr",
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Tombi!",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "An awesome game from the 90's",
						LongDescription:    "Tombi! is an open world platform-adventure game with RPG elements.",
						Categories:         []string{"platforms", "rpg"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tombi!",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com.1.2.5",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tombi.foo.example.com",
						Version:                         "1.2.5",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1997, time.December, 25, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.AvailablePackageSummary{
				{
					AvailablePackageRef: &corev1.AvailablePackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "unknown/tetris.foo.example.com",
					},
					Name:        "tetris.foo.example.com",
					DisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					Categories:       []string{"logging", "daemon-set"},
				},
			},
		},
		{
			name: "it returns empty carvel package summaries if not matching the filters",
			filterOptions: corev1.FilterOptions{
				Query:        "foo",
				Repositories: []string{"foo"},
				Categories:   []string{"foo"},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Tombi!",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "An awesome game from the 90's",
						LongDescription:    "Tombi! is an open world platform-adventure game with RPG elements.",
						Categories:         []string{"platforms", "rpg"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tombi!",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tombi.foo.example.com.1.2.5",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tombi.foo.example.com",
						Version:                         "1.2.5",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1997, time.December, 25, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.AvailablePackageSummary{},
		},
	}

	//nolint:govet
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
						},
						unstructuredObjects...,
					)).
					Build(),
			}

			response, err := s.GetAvailablePackageSummaries(context.Background(), connect.NewRequest(&corev1.GetAvailablePackageSummariesRequest{
				Context:           defaultContext,
				PaginationOptions: &tc.paginationOptions,
				FilterOptions:     &tc.filterOptions,
			}))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %d, want: %d, err: %+v", got, want, err)
			}
			// If we were expecting an error, continue to the next test.
			if tc.expectedErrorCode != 0 {
				return
			}

			if got, want := response.Msg.AvailablePackageSummaries, tc.expectedPackages; !cmp.Equal(got, want, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

func TestGetAvailablePackageVersions(t *testing.T) {
	testCases := []struct {
		name              string
		existingObjects   []k8sruntime.Object
		request           *corev1.GetAvailablePackageVersionsRequest
		expectedErrorCode connect.Code
		expectedResponse  *corev1.GetAvailablePackageVersionsResponse
	}{
		{
			name:              "it returns invalid argument if called without a package reference",
			request:           nil,
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "it returns invalid argument if called without namespace",
			request: &corev1.GetAvailablePackageVersionsRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    &corev1.Context{},
					Identifier: "unknown/package-one",
				},
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "it returns invalid argument if called without an identifier",
			request: &corev1.GetAvailablePackageVersionsRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "kubeapps",
					},
				},
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "it returns the package version summary",
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.7",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.7",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.4",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.4",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			request: &corev1.GetAvailablePackageVersionsRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
					},
					Identifier: "unknown/tetris.foo.example.com",
				},
			},
			expectedResponse: &corev1.GetAvailablePackageVersionsResponse{
				PackageAppVersions: []*corev1.PackageAppVersion{
					{
						PkgVersion: "1.2.7",
						AppVersion: "1.2.7",
					},
					{
						PkgVersion: "1.2.4",
						AppVersion: "1.2.4",
					},
					{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}: pkgResource + "List",
						},
						unstructuredObjects...,
					)).
					Build(),
			}

			response, err := s.GetAvailablePackageVersions(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %+v, want: %+v, err: %+v", got, want, err)
			}

			// We don't need to check anything else for non-OK codes.
			if tc.expectedErrorCode != 0 {
				return
			}

			if got, want := response.Msg, tc.expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

func TestGetAvailablePackageDetail(t *testing.T) {
	testCases := []struct {
		name            string
		existingObjects []k8sruntime.Object
		expectedPackage *corev1.AvailablePackageDetail
		errorCode       connect.Code
		request         *corev1.GetAvailablePackageDetailRequest
	}{
		{
			name: "it returns an availablePackageDetail of the latest version",
			request: &corev1.GetAvailablePackageDetailRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Identifier: "unknown/tetris.foo.example.com",
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackage: &corev1.AvailablePackageDetail{
				Name:            "tetris.foo.example.com",
				DisplayName:     "Classic Tetris",
				LongDescription: "A few sentences but not really a readme",
				Version: &corev1.PackageAppVersion{
					PkgVersion: "1.2.3",
					AppVersion: "1.2.3",
				},
				Maintainers:      []*corev1.Maintainer{{Name: "person1"}, {Name: "person2"}},
				IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
				ShortDescription: "A great game for arcade gamers",
				Categories:       []string{"logging", "daemon-set"},
				Readme: `## Description

A few sentences but not really a readme

## Capactiy requirements

capacity description

## Release notes

release notes

Released at: June, 6 1984

## Support

Some support information

## Licenses

- my-license

`,
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Identifier: "unknown/tetris.foo.example.com",
					Plugin:     &pluginDetail,
				},
			},
		},
		{
			name: "it returns an availablePackageDetail of the latest version with repo-based identifiers",
			request: &corev1.GetAvailablePackageDetailRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Identifier: "unknown/tetris.foo.example.com",
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
						Annotations: map[string]string{
							REPO_REF_ANNOTATION: "default/tce-repo",
						},
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackage: &corev1.AvailablePackageDetail{
				Name:            "tetris.foo.example.com",
				DisplayName:     "Classic Tetris",
				LongDescription: "A few sentences but not really a readme",
				Version: &corev1.PackageAppVersion{
					PkgVersion: "1.2.3",
					AppVersion: "1.2.3",
				},
				Maintainers:      []*corev1.Maintainer{{Name: "person1"}, {Name: "person2"}},
				IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
				ShortDescription: "A great game for arcade gamers",
				Categories:       []string{"logging", "daemon-set"},
				Readme: `## Description

A few sentences but not really a readme

## Capactiy requirements

capacity description

## Release notes

release notes

Released at: June, 6 1984

## Support

Some support information

## Licenses

- my-license

`,
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Identifier: "tce-repo/tetris.foo.example.com",
					Plugin:     &pluginDetail,
				},
			},
		},
		{
			name: "it combines long description and support description for readme field",
			request: &corev1.GetAvailablePackageDetailRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Identifier: "unknown/tetris.foo.example.com",
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackage: &corev1.AvailablePackageDetail{
				Name:            "tetris.foo.example.com",
				DisplayName:     "Classic Tetris",
				LongDescription: "A few sentences but not really a readme",
				Version: &corev1.PackageAppVersion{
					PkgVersion: "1.2.3",
					AppVersion: "1.2.3",
				},
				Maintainers:      []*corev1.Maintainer{{Name: "person1"}, {Name: "person2"}},
				IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
				ShortDescription: "A great game for arcade gamers",
				Categories:       []string{"logging", "daemon-set"},
				Readme: `## Description

A few sentences but not really a readme

## Capactiy requirements

capacity description

## Release notes

release notes

Released at: June, 6 1984

## Support

Some support information

## Licenses

- my-license

`,
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Identifier: "unknown/tetris.foo.example.com",
					Plugin:     &pluginDetail,
				},
			},
		},
		{
			name: "it returns an invalid arg error status if no context is provided",
			request: &corev1.GetAvailablePackageDetailRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Identifier: "unknown/foo/bar",
				},
			},
			errorCode: connect.CodeInvalidArgument,
		},
		{
			name: "it returns not found error status if the requested package version doesn't exist",
			request: &corev1.GetAvailablePackageDetailRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersion: "1.2.4",
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			errorCode: connect.CodeNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
						},
						unstructuredObjects...,
					)).Build(),
			}
			availablePackageDetail, err := s.GetAvailablePackageDetail(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.errorCode; err != nil && got != want {
				t.Fatalf("got: %+v, want: %+v, err: %+v", got, want, err)
			}

			if tc.errorCode == 0 {
				if got, want := availablePackageDetail.Msg.AvailablePackageDetail, tc.expectedPackage; !cmp.Equal(got, want, ignoreUnexported) {
					t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
				}
			}
		})
	}
}

// installed packages

func TestGetInstalledPackageSummaries(t *testing.T) {
	testCases := []struct {
		name              string
		request           *corev1.GetInstalledPackageSummariesRequest
		existingObjects   []k8sruntime.Object
		expectedPackages  []*corev1.InstalledPackageSummary
		expectedErrorCode connect.Code
	}{
		{
			name: "it returns an error if a non-existent page is requested",
			request: &corev1.GetInstalledPackageSummariesRequest{
				Context: defaultContext,
				PaginationOptions: &corev1.PaginationOptions{
					PageToken: "2",
					PageSize:  2,
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name:    "it returns carvel empty installed package summary when no package install is present",
			request: &corev1.GetInstalledPackageSummariesRequest{Context: defaultContext},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{},
		},
		{
			name:    "it returns carvel installed package summary with complete metadata",
			request: &corev1.GetInstalledPackageSummariesRequest{Context: defaultContext},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{
				{
					InstalledPackageRef: &corev1.InstalledPackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "my-installation",
					},
					Name:           "my-installation",
					PkgDisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:             "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription:    "A great game for arcade gamers",
					PkgVersionReference: &corev1.VersionReference{Version: "1.2.3"},
					CurrentVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					LatestMatchingVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					Status: &corev1.InstalledPackageStatus{
						Ready:      true,
						Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
						UserReason: "Deployed",
					},
				},
			},
		},
		{
			name: "it returns carvel installed package summary with complete metadata from global namespace",
			// Request in test has to use empty namespace to search across all
			// namespaces since in real life the metadata and package resources
			// are not actual CRs but aggregated APIs that return data across
			// namespaces.
			request: &corev1.GetInstalledPackageSummariesRequest{
				Context: &corev1.Context{
					Namespace: "",
					Cluster:   defaultContext.Cluster,
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: demoGlobalPackagingNamespace,
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: demoGlobalPackagingNamespace,
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{
				{
					InstalledPackageRef: &corev1.InstalledPackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "my-installation",
					},
					Name:           "my-installation",
					PkgDisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:             "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription:    "A great game for arcade gamers",
					PkgVersionReference: &corev1.VersionReference{Version: "1.2.3"},
					CurrentVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					LatestMatchingVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					Status: &corev1.InstalledPackageStatus{
						Ready:      true,
						Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
						UserReason: "Deployed",
					},
				},
			},
		},
		{
			name: "it ignores carvel package install without any related metadata",
			request: &corev1.GetInstalledPackageSummariesRequest{
				Context: &corev1.Context{
					Namespace: "",
					Cluster:   defaultContext.Cluster,
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: demoGlobalPackagingNamespace,
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{},
		},
		{
			name: "it returns carvel installed package from different namespaces if context.namespace==''",
			request: &corev1.GetInstalledPackageSummariesRequest{
				Context: &corev1.Context{
					Namespace: "",
					Cluster:   defaultContext.Cluster,
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "another-ns",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "another-ns",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "foo",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "another-ns",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "foo",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{
				{
					InstalledPackageRef: &corev1.InstalledPackageReference{
						Context:    &corev1.Context{Namespace: "another-ns", Cluster: defaultContext.Cluster},
						Plugin:     &pluginDetail,
						Identifier: "my-installation",
					},
					Name:           "my-installation",
					PkgDisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:             "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription:    "A great game for arcade gamers",
					PkgVersionReference: &corev1.VersionReference{Version: "1.2.3"},
					CurrentVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					LatestMatchingVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					Status: &corev1.InstalledPackageStatus{
						Ready:      true,
						Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
						UserReason: "Deployed",
					},
				},
				{
					InstalledPackageRef: &corev1.InstalledPackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "my-installation",
					},
					Name:           "my-installation",
					PkgDisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:             "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription:    "A great game for arcade gamers",
					PkgVersionReference: &corev1.VersionReference{Version: "1.2.3"},
					CurrentVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					LatestMatchingVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					Status: &corev1.InstalledPackageStatus{
						Ready:      true,
						Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
						UserReason: "Deployed",
					},
				},
			},
		},
		{
			name:    "it returns carvel installed package summary with a packageInstall without status",
			request: &corev1.GetInstalledPackageSummariesRequest{Context: defaultContext},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{
				{
					InstalledPackageRef: &corev1.InstalledPackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "my-installation",
					},
					Name:           "my-installation",
					PkgDisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					IconUrl:          "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription: "A great game for arcade gamers",
					PkgVersionReference: &corev1.VersionReference{
						Version: "",
					},
					CurrentVersion: &corev1.PackageAppVersion{
						PkgVersion: "",
						AppVersion: "",
					},
					LatestMatchingVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					Status: &corev1.InstalledPackageStatus{
						Ready:      false,
						Reason:     corev1.InstalledPackageStatus_STATUS_REASON_PENDING,
						UserReason: "No status information yet",
					},
				},
			},
		},
		{
			name:    "it returns the latest semver version in the latest version field with the latest matching version",
			request: &corev1.GetInstalledPackageSummariesRequest{Context: defaultContext},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.7",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.7",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.2.0.0",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "2.0.0",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: ">1.0.0 <2.0.0",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{
				{
					InstalledPackageRef: &corev1.InstalledPackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "my-installation",
					},
					Name:           "my-installation",
					PkgDisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "2.0.0",
						AppVersion: "2.0.0",
					},
					IconUrl:             "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription:    "A great game for arcade gamers",
					PkgVersionReference: &corev1.VersionReference{Version: "1.2.3"},
					CurrentVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					LatestMatchingVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.7",
						AppVersion: "1.2.7",
					},
					Status: &corev1.InstalledPackageStatus{
						Ready:      true,
						Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
						UserReason: "Deployed",
					},
				},
			},
		},
		{
			name:    "it returns the latest semver version in the latest version field with no latest matching version if constraint is not satisfied ",
			request: &corev1.GetInstalledPackageSummariesRequest{Context: defaultContext},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.7",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.7",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "9.9.9",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedPackages: []*corev1.InstalledPackageSummary{
				{
					InstalledPackageRef: &corev1.InstalledPackageReference{
						Context:    defaultContext,
						Plugin:     &pluginDetail,
						Identifier: "my-installation",
					},
					Name:           "my-installation",
					PkgDisplayName: "Classic Tetris",
					LatestVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.7",
						AppVersion: "1.2.7",
					},
					IconUrl:             "data:image/svg+xml;base64,Tm90IHJlYWxseSBTVkcK",
					ShortDescription:    "A great game for arcade gamers",
					PkgVersionReference: &corev1.VersionReference{Version: "1.2.3"},
					CurrentVersion: &corev1.PackageAppVersion{
						PkgVersion: "1.2.3",
						AppVersion: "1.2.3",
					},
					Status: &corev1.InstalledPackageStatus{
						Ready:      true,
						Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
						UserReason: "Deployed",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
							{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgInstallsResource}:          pkgInstallResource + "List",
						},
						unstructuredObjects...,
					)).Build(),
			}

			response, err := s.GetInstalledPackageSummaries(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %d, want: %d, err: %+v", got, want, err)
			}
			// If we were expecting an error, continue to the next test.
			if tc.expectedErrorCode != 0 {
				return
			}

			if got, want := response.Msg.InstalledPackageSummaries, tc.expectedPackages; !cmp.Equal(got, want, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

func TestGetInstalledPackageDetail(t *testing.T) {
	testCases := []struct {
		name                 string
		existingObjects      []k8sruntime.Object
		existingTypedObjects []k8sruntime.Object
		expectedPackage      *corev1.InstalledPackageDetail
		errorCode            connect.Code
		request              *corev1.GetInstalledPackageDetailRequest
	}{
		{
			name: "it returns carvel installed package detail with the latest matching version",
			request: &corev1.GetInstalledPackageDetailRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.7",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.7",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.2.0.0",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "2.0.0",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: ">1.0.0 <2.0.0",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-default-values",
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"values.yaml": []byte("foo: bar"),
					},
				},
			},
			expectedPackage: &corev1.InstalledPackageDetail{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
				Name: "my-installation",
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				CurrentVersion: &corev1.PackageAppVersion{
					PkgVersion: "1.2.3",
					AppVersion: "1.2.3",
				},
				ValuesApplied: "\n# values.yaml\nfoo: bar\n",
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
					Interval:           "30s",
					Suspend:            false,
				},
				Status: &corev1.InstalledPackageStatus{
					Ready:      true,
					Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
					UserReason: "Deployed",
				},
				PostInstallationNotes: strings.ReplaceAll(`#### Deploy

<x60><x60><x60>
deployStdout
<x60><x60><x60>

#### Fetch

<x60><x60><x60>
fetchStdout
<x60><x60><x60>

### Errors

#### Deploy

<x60><x60><x60>
deployStderr
<x60><x60><x60>

#### Fetch

<x60><x60><x60>
fetchStderr
<x60><x60><x60>

`, "<x60>", "`"),
				LatestMatchingVersion: &corev1.PackageAppVersion{
					PkgVersion: "1.2.7",
					AppVersion: "1.2.7",
				},
				LatestVersion: &corev1.PackageAppVersion{
					PkgVersion: "2.0.0",
					AppVersion: "2.0.0",
				},
			},
		},
		{
			name: "it returns carvel installed package detail with the latest matching version and repo-based identifiers",
			request: &corev1.GetInstalledPackageDetailRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
						Annotations: map[string]string{
							REPO_REF_ANNOTATION: "default/tce-repo",
						},
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.7",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.7",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.2.0.0",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "2.0.0",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: ">1.0.0 <2.0.0",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-default-values",
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"values.yaml": []byte("foo: bar"),
					},
				},
			},
			expectedPackage: &corev1.InstalledPackageDetail{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "tce-repo/tetris.foo.example.com",
				},
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
				Name: "my-installation",
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				CurrentVersion: &corev1.PackageAppVersion{
					PkgVersion: "1.2.3",
					AppVersion: "1.2.3",
				},
				ValuesApplied: "\n# values.yaml\nfoo: bar\n",
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
					Interval:           "30s",
					Suspend:            false,
				},
				Status: &corev1.InstalledPackageStatus{
					Ready:      true,
					Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
					UserReason: "Deployed",
				},
				PostInstallationNotes: strings.ReplaceAll(`#### Deploy

<x60><x60><x60>
deployStdout
<x60><x60><x60>

#### Fetch

<x60><x60><x60>
fetchStdout
<x60><x60><x60>

### Errors

#### Deploy

<x60><x60><x60>
deployStderr
<x60><x60><x60>

#### Fetch

<x60><x60><x60>
fetchStderr
<x60><x60><x60>

`, "<x60>", "`"),
				LatestMatchingVersion: &corev1.PackageAppVersion{
					PkgVersion: "1.2.7",
					AppVersion: "1.2.7",
				},
				LatestVersion: &corev1.PackageAppVersion{
					PkgVersion: "2.0.0",
					AppVersion: "2.0.0",
				},
			},
		},
		{
			name: "it returns carvel installed package detail with no latest matching version if constraint is not satisfied",
			request: &corev1.GetInstalledPackageDetailRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "9.9.9",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-default-values",
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"values.yaml": []byte("foo: bar"),
					},
				},
			},
			expectedPackage: &corev1.InstalledPackageDetail{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
				Name: "my-installation",
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				CurrentVersion: &corev1.PackageAppVersion{
					PkgVersion: "1.2.3",
					AppVersion: "1.2.3",
				},
				ValuesApplied: "\n# values.yaml\nfoo: bar\n",
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
					Interval:           "30s",
					Suspend:            false,
				},
				Status: &corev1.InstalledPackageStatus{
					Ready:      true,
					Reason:     corev1.InstalledPackageStatus_STATUS_REASON_INSTALLED,
					UserReason: "Deployed",
				},
				PostInstallationNotes: strings.ReplaceAll(`#### Deploy

<x60><x60><x60>
deployStdout
<x60><x60><x60>

#### Fetch

<x60><x60><x60>
fetchStdout
<x60><x60><x60>

### Errors

#### Deploy

<x60><x60><x60>
deployStderr
<x60><x60><x60>

#### Fetch

<x60><x60><x60>
fetchStderr
<x60><x60><x60>

`, "<x60>", "`"),
				LatestVersion: &corev1.PackageAppVersion{
					PkgVersion: "1.2.3",
					AppVersion: "1.2.3",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typfake.NewSimpleClientset(tc.existingTypedObjects...)).
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
						},
						unstructuredObjects...,
					)).Build(),
			}
			installedPackageDetail, err := s.GetInstalledPackageDetail(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.errorCode; err != nil && got != want {
				t.Fatalf("got: %+v, want: %+v, err: %+v", got, want, err)
			}

			if tc.errorCode == 0 {
				if got, want := installedPackageDetail.Msg.InstalledPackageDetail, tc.expectedPackage; !cmp.Equal(got, want, ignoreUnexported) {
					t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
				}
			}
		})
	}
}

func TestCreateInstalledPackage(t *testing.T) {
	testCases := []struct {
		name                   string
		request                *corev1.CreateInstalledPackageRequest
		pluginConfig           *kappControllerPluginParsedConfig
		existingObjects        []k8sruntime.Object
		existingTypedObjects   []k8sruntime.Object
		expectedErrorCode      connect.Code
		expectedResponse       *corev1.CreateInstalledPackageResponse
		expectedPackageInstall *packagingv1alpha1.PackageInstall
	}{
		{
			name: "create installed package",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: defaultPluginConfig,
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.2.3",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package with error (kapp App not being created)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				timeoutSeconds:                     1, //to avoid unnecessary test delays
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedErrorCode: connect.CodeInternal,
		},
		{
			name: "create installed package (with values)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				Name:   "my-installation",
				Values: "foo: bar",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: defaultPluginConfig,
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.2.3",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (with reconciliationOptions)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					Interval:           "99s",
					Suspend:            true,
					ServiceAccountName: "my-sa",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
			},
			pluginConfig: defaultPluginConfig,
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "my-sa",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.2.3",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     true,
					Canceled:   false,
					SyncPeriod: &metav1.Duration{Duration: (time.Second * 99)},
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (prereleases - defaultPrereleasesVersionSelection: nil)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: nil,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.0.0",
							Prereleases: nil,
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (non eligible version)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0-rc1",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: nil,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "create installed package (prereleases - defaultPrereleasesVersionSelection: [])",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: []string{},
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.0.0",
							Prereleases: &vendirversions.VersionSelectionSemverPrereleases{},
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (prereleases - defaultPrereleasesVersionSelection: ['rc'])",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: []string{"rc"},
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.0.0",
							Prereleases: &vendirversions.VersionSelectionSemverPrereleases{Identifiers: []string{"rc"}},
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (version constraint - upgradePolicy: none)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: defaultPluginConfig,
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.0.0",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (version constraint - upgradePolicy: major)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               pkgutils.UpgradePolicyMajor,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: ">=1.0.0",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (version constraint - upgradePolicy: minor)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               pkgutils.UpgradePolicyMinor,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: ">=1.0.0 <2.0.0",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (version constraint - upgradePolicy: patch)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               pkgutils.UpgradePolicyPatch,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: ">=1.0.0 <1.1.0",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
		{
			name: "create installed package (defaultAllowDowngrades: true)",
			request: &corev1.CreateInstalledPackageRequest{
				AvailablePackageRef: &corev1.AvailablePackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "unknown/tetris.foo.example.com",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.0.0",
				},
				Name: "my-installation",
				TargetContext: &corev1.Context{
					Namespace: "default",
					Cluster:   "default",
				},
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             true,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&kappctrlv1alpha1.App{
					TypeMeta: metav1.TypeMeta{
						Kind:       appResource,
						APIVersion: kappctrlAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: kappctrlv1alpha1.AppSpec{
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					},
					Status: kappctrlv1alpha1.AppStatus{
						Deploy: &kappctrlv1alpha1.AppStatusDeploy{
							Stdout: "deployStdout",
							Stderr: "deployStderr",
						},
						Fetch: &kappctrlv1alpha1.AppStatusFetch{
							Stdout: "fetchStdout",
							Stderr: "fetchStderr",
						},
						Inspect: &kappctrlv1alpha1.AppStatusInspect{
							Stdout: "inspectStdout",
							Stderr: "inspectStderr",
						},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.CreateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   "default",
					Name:        "my-installation",
					Annotations: map[string]string{kappctrlpackageinstall.DowngradableAnnKey: ""},
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.0.0",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: nil,
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration:  0,
						Conditions:          nil,
						FriendlyDescription: "",
						UsefulErrorMessage:  "",
					},
					Version:              "",
					LastAttemptedVersion: "",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			dynamicClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(
				k8sruntime.NewScheme(),
				map[schema.GroupVersionResource]string{
					{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
					{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgInstallsResource}:          pkgInstallResource + "List",
				},
				unstructuredObjects...,
			)

			s := Server{
				pluginConfig: tc.pluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typfake.NewSimpleClientset(tc.existingTypedObjects...)).
					WithDynamic(dynamicClient).
					Build(),
			}

			createInstalledPackageResponse, err := s.CreateInstalledPackage(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %d, want: %d, err: %+v", got, want, err)
			}
			// If we were expecting an error, continue to the next test.
			if tc.expectedErrorCode != 0 {
				return
			}
			if tc.expectedPackageInstall != nil {
				if got, want := createInstalledPackageResponse.Msg, tc.expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
					t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
				}

				createdPkgInstall, err := s.getPkgInstall(context.Background(), http.Header{}, "default", tc.request.TargetContext.Namespace, createInstalledPackageResponse.Msg.InstalledPackageRef.Identifier)
				if err != nil {
					t.Fatalf("%+v", err)
				}

				if got, want := createdPkgInstall, tc.expectedPackageInstall; !cmp.Equal(want, got, ignoreUnexported) {
					t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
				}
			}
		})
	}
}

func TestUpdateInstalledPackage(t *testing.T) {
	testCases := []struct {
		name                   string
		request                *corev1.UpdateInstalledPackageRequest
		pluginConfig           *kappControllerPluginParsedConfig
		existingObjects        []k8sruntime.Object
		existingTypedObjects   []k8sruntime.Object
		expectedErrorCode      connect.Code
		expectedResponse       *corev1.UpdateInstalledPackageResponse
		expectedPackageInstall *packagingv1alpha1.PackageInstall
	}{
		{
			name: "update installed package",
			request: &corev1.UpdateInstalledPackageRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3",
				},
				Values: "foo: bar",
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
					Interval:           "30s",
					Suspend:            false,
				},
			},
			pluginConfig: defaultPluginConfig,
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-default-values",
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"values.yaml": []byte("foo: bar"),
					},
				},
			},
			expectedResponse: &corev1.UpdateInstalledPackageResponse{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			expectedPackageInstall: &packagingv1alpha1.PackageInstall{
				TypeMeta: metav1.TypeMeta{
					Kind:       pkgInstallResource,
					APIVersion: packagingAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "my-installation",
				},
				Spec: packagingv1alpha1.PackageInstallSpec{
					ServiceAccountName: "default",
					PackageRef: &packagingv1alpha1.PackageRef{
						RefName: "tetris.foo.example.com",
						VersionSelection: &vendirversions.VersionSelectionSemver{
							Constraints: "1.2.3",
						},
					},
					Values: []packagingv1alpha1.PackageInstallValues{{
						SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
							Name: "my-installation-default-values",
						},
					},
					},
					Paused:     false,
					Canceled:   false,
					SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
					NoopDelete: false,
				},
				Status: packagingv1alpha1.PackageInstallStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						ObservedGeneration: 1,
						Conditions: []kappctrlv1alpha1.Condition{{
							Type:    kappctrlv1alpha1.ReconcileSucceeded,
							Status:  k8scorev1.ConditionTrue,
							Reason:  "baz",
							Message: "qux",
						}},
						FriendlyDescription: "foo",
						UsefulErrorMessage:  "Deployed",
					},
					Version:              "1.2.3",
					LastAttemptedVersion: "1.2.3",
				},
			},
		},
		{
			name: "update installed package (non eligible version)",
			request: &corev1.UpdateInstalledPackageRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context: &corev1.Context{
						Namespace: "default",
						Cluster:   "default",
					},
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
				PkgVersionReference: &corev1.VersionReference{
					Version: "1.2.3-rc",
				},
				Values: "foo: bar",
				ReconciliationOptions: &corev1.ReconciliationOptions{
					ServiceAccountName: "default",
					Interval:           "30s",
					Suspend:            false,
				},
			},
			pluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: nil,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			existingObjects: []k8sruntime.Object{
				&datapackagingv1alpha1.PackageMetadata{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgMetadataResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com",
					},
					Spec: datapackagingv1alpha1.PackageMetadataSpec{
						DisplayName:        "Classic Tetris",
						IconSVGBase64:      "Tm90IHJlYWxseSBTVkcK",
						ShortDescription:   "A great game for arcade gamers",
						LongDescription:    "A few sentences but not really a readme",
						Categories:         []string{"logging", "daemon-set"},
						Maintainers:        []datapackagingv1alpha1.Maintainer{{Name: "person1"}, {Name: "person2"}},
						SupportDescription: "Some support information",
						ProviderName:       "Tetris inc.",
					},
				},
				&datapackagingv1alpha1.Package{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgResource,
						APIVersion: datapackagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "tetris.foo.example.com.1.2.3",
					},
					Spec: datapackagingv1alpha1.PackageSpec{
						RefName:                         "tetris.foo.example.com",
						Version:                         "1.2.3",
						Licenses:                        []string{"my-license"},
						ReleaseNotes:                    "release notes",
						CapactiyRequirementsDescription: "capacity description",
						ReleasedAt:                      metav1.Time{Time: time.Date(1984, time.June, 6, 0, 0, 0, 0, time.UTC)},
					},
				},
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-default-values",
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"values.yaml": []byte("foo: bar"),
					},
				},
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typfake.NewSimpleClientset(tc.existingTypedObjects...)).
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
							{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgInstallsResource}:          pkgInstallResource + "List",
						},
						unstructuredObjects...,
					)).Build(),
			}

			updateInstalledPackageResponse, err := s.UpdateInstalledPackage(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %d, want: %d, err: %+v", got, want, err)
			}
			// If we were expecting an error, continue to the next test.
			if tc.expectedErrorCode != 0 {
				return
			}
			if tc.expectedPackageInstall != nil {
				if got, want := updateInstalledPackageResponse.Msg, tc.expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
					t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
				}

				updatedPkgInstall, err := s.getPkgInstall(context.Background(), http.Header{}, "default", updateInstalledPackageResponse.Msg.InstalledPackageRef.Context.Namespace, updateInstalledPackageResponse.Msg.InstalledPackageRef.Identifier)
				if err != nil {
					t.Fatalf("%+v", err)
				}

				if got, want := updatedPkgInstall, tc.expectedPackageInstall; !cmp.Equal(want, got, ignoreUnexported) {
					t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
				}
			}
		})
	}
}

func TestDeleteInstalledPackage(t *testing.T) {
	testCases := []struct {
		name                 string
		request              *corev1.DeleteInstalledPackageRequest
		existingObjects      []k8sruntime.Object
		existingTypedObjects []k8sruntime.Object
		expectedErrorCode    connect.Code
		expectedResponse     *corev1.DeleteInstalledPackageResponse
	}{
		{
			name: "deletes installed package",
			request: &corev1.DeleteInstalledPackageRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-default-values",
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"values.yaml": []byte("foo: bar"),
					},
				},
			},
			expectedResponse: &corev1.DeleteInstalledPackageResponse{},
		},
		{
			name: "returns not found if installed package doesn't exist",
			request: &corev1.DeleteInstalledPackageRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "noy-my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "Deployed",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-default-values",
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"values.yaml": []byte("foo: bar"),
					},
				},
			},
			expectedErrorCode: connect.CodeNotFound,
			expectedResponse:  &corev1.DeleteInstalledPackageResponse{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typfake.NewSimpleClientset(tc.existingTypedObjects...)).
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
							{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
							{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgInstallsResource}:          pkgInstallResource + "List",
						},
						unstructuredObjects...,
					)).
					Build(),
			}

			deleteInstalledPackageResponse, err := s.DeleteInstalledPackage(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %d, want: %d, err: %+v", got, want, err)
			}
			// If we were expecting an error, continue to the next test.
			if tc.expectedErrorCode != 0 {
				return
			}
			if got, want := deleteInstalledPackageResponse.Msg, tc.expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

func TestGetInstalledPackageResourceRefs(t *testing.T) {
	testCases := []struct {
		name                 string
		request              *corev1.GetInstalledPackageResourceRefsRequest
		existingObjects      []k8sruntime.Object
		existingTypedObjects []k8sruntime.Object
		expectedErrorCode    connect.Code
		expectedResponse     *corev1.GetInstalledPackageResourceRefsResponse
	}{
		{
			name: "fetch the resources from an installed package (kapp < 0.47 suffix)",
			request: &corev1.GetInstalledPackageResourceRefsRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "foo",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
				// Although it's a typical k8s object, it is retrieved with the dynamic client
				&k8scorev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1",
						Kind:       "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-pod",
						Labels:    map[string]string{"kapp.k14s.io/app": "my-id"},
					},
					Spec: k8scorev1.PodSpec{
						Containers: []k8scorev1.Container{{
							Name: "my-installation-container",
						}},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.GetInstalledPackageResourceRefsResponse{
				ResourceRefs: []*corev1.ResourceRef{
					{
						ApiVersion: "v1",
						Kind:       "Pod",
						Name:       "my-installation-pod",
						Namespace:  "default",
					},
				},
				Context: defaultContext,
			},
		},
		{
			name: "fetch the resources from an installed package (kapp => 0.47 suffix)",
			request: &corev1.GetInstalledPackageResourceRefsRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "foo",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
				// Although it's a typical k8s object, it is retrieved with the dynamic client
				&k8scorev1.Pod{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1",
						Kind:       "Pod",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-pod",
						Labels:    map[string]string{"kapp.k14s.io/app": "my-id"},
					},
					Spec: k8scorev1.PodSpec{
						Containers: []k8scorev1.Container{{
							Name: "my-installation-container",
						}},
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation.app",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedResponse: &corev1.GetInstalledPackageResourceRefsResponse{
				ResourceRefs: []*corev1.ResourceRef{
					{
						ApiVersion: "v1",
						Kind:       "Pod",
						Name:       "my-installation-pod",
						Namespace:  "default",
					},
				},
				Context: defaultContext,
			},
		},
		{
			name: "returns NotFound if the app configmap is not yet available",
			request: &corev1.GetInstalledPackageResourceRefsRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: (time.Second * 30)},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "foo",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			expectedErrorCode: connect.CodeNotFound,
		},
		{
			name: "returns NotFound if app exists but no resources found",
			request: &corev1.GetInstalledPackageResourceRefsRequest{
				InstalledPackageRef: &corev1.InstalledPackageReference{
					Context:    defaultContext,
					Plugin:     &pluginDetail,
					Identifier: "my-installation",
				},
			},
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageInstall{
					TypeMeta: metav1.TypeMeta{
						Kind:       pkgInstallResource,
						APIVersion: packagingAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation",
					},
					Spec: packagingv1alpha1.PackageInstallSpec{
						ServiceAccountName: "default",
						PackageRef: &packagingv1alpha1.PackageRef{
							RefName: "tetris.foo.example.com",
							VersionSelection: &vendirversions.VersionSelectionSemver{
								Constraints: "1.2.3",
							},
						},
						Values: []packagingv1alpha1.PackageInstallValues{{
							SecretRef: &packagingv1alpha1.PackageInstallValuesSecretRef{
								Name: "my-installation-default-values",
							},
						},
						},
						Paused:     false,
						Canceled:   false,
						SyncPeriod: &metav1.Duration{Duration: time.Second * 30},
						NoopDelete: false,
					},
					Status: packagingv1alpha1.PackageInstallStatus{
						GenericStatus: kappctrlv1alpha1.GenericStatus{
							ObservedGeneration: 1,
							Conditions: []kappctrlv1alpha1.Condition{{
								Type:    kappctrlv1alpha1.ReconcileSucceeded,
								Status:  k8scorev1.ConditionTrue,
								Reason:  "baz",
								Message: "qux",
							}},
							FriendlyDescription: "foo",
							UsefulErrorMessage:  "foo",
						},
						Version:              "1.2.3",
						LastAttemptedVersion: "1.2.3",
					},
				},
			},
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ConfigMap",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "my-installation-ctrl",
					},
					Data: map[string]string{
						"spec": "{\"labelKey\":\"kapp.k14s.io/app\",\"labelValue\":\"my-id\"}",
					},
				},
			},
			expectedErrorCode: connect.CodeNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}
			// If more resources types are added, this will need to be updated accordingly
			apiResources := []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{Name: "pods", Namespaced: true, Kind: "Pod", Verbs: []string{"list", "get"}},
						{Name: "configmaps", Namespaced: true, Kind: "ConfigMap", Verbs: []string{"list", "get"}},
					},
				},
			}

			typedClient := typfake.NewSimpleClientset(tc.existingTypedObjects...)

			// We cast the dynamic client to a fake client, so we can set the response
			fakeDiscovery, _ := typedClient.Discovery().(*disfake.FakeDiscovery)
			fakeDiscovery.Fake.Resources = apiResources

			dynClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(
				k8sruntime.NewScheme(),
				map[schema.GroupVersionResource]string{
					{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgsResource}:         pkgResource + "List",
					{Group: datapackagingv1alpha1.SchemeGroupVersion.Group, Version: datapackagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgMetadatasResource}: pkgMetadataResource + "List",
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgInstallsResource}:          pkgInstallResource + "List",
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: appsResource}:                 appResource + "List",
					// If more resources types are added, this will need to be updated accordingly
					{Group: "", Version: "v1", Resource: "pods"}:       "Pod" + "List",
					{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMap" + "List",
				},
				unstructuredObjects...,
			)

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typedClient).
					WithDynamic(dynClient).
					Build(),
				kappClientsGetter: func(headers http.Header, cluster, namespace string) (ctlapp.Apps, ctlres.IdentifiedResources, *kappcmdapp.FailingAPIServicesPolicy, ctlres.ResourceFilter, error) {
					// Create a fake Kapp DepsFactory and configure there the fake k8s clients the hereinbefore created
					depsFactory := NewFakeDepsFactoryImpl()
					depsFactory.SetCoreClient(typedClient)
					depsFactory.SetDynamicClient(dynClient)
					// The rest of the logic remain unchanged as in the real server.go file (DRY it up?)
					resourceFilterFlags := kappcmdtools.ResourceFilterFlags{}
					resourceFilter, err := resourceFilterFlags.ResourceFilter()
					if err != nil {
						return ctlapp.Apps{}, ctlres.IdentifiedResources{}, nil, ctlres.ResourceFilter{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("unable to get config due to: %w", err))
					}
					resourceTypesFlags := kappcmdapp.ResourceTypesFlags{
						IgnoreFailingAPIServices:         true,
						ScopeToFallbackAllowedNamespaces: true,
					}
					failingAPIServicesPolicy := resourceTypesFlags.FailingAPIServicePolicy()
					supportingNsObjs, err := kappcmdapp.FactoryClients(depsFactory, kappcmdcore.NamespaceFlags{Name: namespace}, resourceTypesFlags, logger.NewNoopLogger())
					if err != nil {
						return ctlapp.Apps{}, ctlres.IdentifiedResources{}, nil, ctlres.ResourceFilter{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("unable to get config due to: %w", err))
					}
					supportingObjs, err := kappcmdapp.FactoryClients(depsFactory, kappcmdcore.NamespaceFlags{Name: ""}, resourceTypesFlags, logger.NewNoopLogger())
					if err != nil {
						return ctlapp.Apps{}, ctlres.IdentifiedResources{}, nil, ctlres.ResourceFilter{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("unable to get config due to: %w", err))
					}
					return supportingNsObjs.Apps, supportingObjs.IdentifiedResources, failingAPIServicesPolicy, resourceFilter, nil
				},
			}

			getInstalledPackageResourceRefsResponse, err := s.GetInstalledPackageResourceRefs(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %d, want: %d, err: %+v", got, want, err)
			}
			// If we were expecting an error, continue to the next test.
			if tc.expectedErrorCode != 0 {
				return
			}
			if got, want := getInstalledPackageResourceRefsResponse.Msg, tc.expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

// repositories

func TestAddPackageRepository(t *testing.T) {
	defaultRef := &corev1.PackageRepositoryReference{
		Context:    defaultGlobalContext,
		Plugin:     &pluginDetail,
		Identifier: "globalrepo",
	}
	defaultRequest := func() *corev1.AddPackageRepositoryRequest {
		return &corev1.AddPackageRepositoryRequest{
			Context:  defaultGlobalContext,
			Name:     "globalrepo",
			Type:     typeImgPkgBundle,
			Url:      "projects.registry.example.com/repo-1/main@sha256:abcd",
			Interval: "24h",
			Plugin:   &pluginDetail,
		}
	}
	defaultRepository := func() *packagingv1alpha1.PackageRepository {
		return &packagingv1alpha1.PackageRepository{
			TypeMeta:   defaultTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
			Spec: packagingv1alpha1.PackageRepositorySpec{
				SyncPeriod: &metav1.Duration{Duration: time.Duration(24) * time.Hour},
				Fetch: &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
			Status: packagingv1alpha1.PackageRepositoryStatus{},
		}
	}

	testCases := []struct {
		name                 string
		existingObjects      []k8sruntime.Object
		existingTypedObjects []k8sruntime.Object
		requestCustomizer    func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest
		repositoryCustomizer func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository
		expectedErrorCode    connect.Code
		expectedErrorString  string
		expectedRef          *corev1.PackageRepositoryReference
		customChecks         func(t *testing.T, s *Server)
	}{
		{
			name: "validate cluster",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Context = &corev1.Context{Cluster: "other", Namespace: demoGlobalPackagingNamespace}
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate name",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Name = ""
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate scope",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.NamespaceScoped = true
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate scope",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Context = &corev1.Context{Namespace: "foo", Cluster: defaultContext.Cluster}
				request.NamespaceScoped = false
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate tls config",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.TlsConfig = &corev1.PackageRepositoryTlsConfig{}
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate exists in global ns",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				return request
			},
			expectedErrorCode: connect.CodeAlreadyExists,
		},
		{
			name: "validate exists in private ns",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "nsrepo", Namespace: "privatens"},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Context = &corev1.Context{Namespace: "privatens", Cluster: defaultContext.Cluster}
				request.Name = "nsrepo"
				request.NamespaceScoped = true
				return request
			},
			expectedErrorCode: connect.CodeAlreadyExists,
		},
		{
			name: "validate url",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Url = ""
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate type (empty)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = ""
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate type (invalid)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = "othertype"
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate type (inline)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = typeInline
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate details (invalid type)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.CustomDetail, _ = anypb.New(&corev1.AddPackageRepositoryRequest{})
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate details (type mismatch)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Http: &kappcorev1.PackageRepositoryHttp{
							SubPath: "packages",
							Sha256:  "ABC",
						},
					},
				})
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate auth (type incompatibility)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_SSH,
				}
				return request
			},
			expectedErrorCode:   connect.CodeInvalidArgument,
			expectedErrorString: "Auth Type is incompatible",
		},
		{
			name: "validate auth (user managed, invalid secret)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{},
					},
				}
				return request
			},
			expectedErrorCode:   connect.CodeInvalidArgument,
			expectedErrorString: "the secret name is not provided",
		},
		{
			name: "validate auth (user managed, secret does not exist)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret",
						},
					},
				}
				return request
			},
			expectedErrorCode:   connect.CodeInvalidArgument,
			expectedErrorString: "not found",
		},
		{
			name: "validate auth (user managed, secret is incompatible)",
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: defaultGlobalContext.Namespace, Name: "my-secret"},
					Data:       map[string][]byte{k8scorev1.BasicAuthUsernameKey: []byte("foo"), k8scorev1.BasicAuthPasswordKey: []byte("bar")},
				},
			},
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_DOCKER_CONFIG_JSON,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret",
						},
					},
				}
				return request
			},
			expectedErrorCode:   connect.CodeInvalidArgument,
			expectedErrorString: "the secret does not match",
		},
		{
			name: "validate auth (plugin managed, invalid config, basic auth)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
				}
				return request
			},
			expectedErrorCode:   connect.CodeInvalidArgument,
			expectedErrorString: "Missing basic auth",
		},
		{
			name: "validate auth (plugin managed, invalid config, docker)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_DOCKER_CONFIG_JSON,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_DockerCreds{
						DockerCreds: &corev1.DockerCredentials{},
					},
				}
				return request
			},
			expectedErrorCode:   connect.CodeInvalidArgument,
			expectedErrorString: "Missing Docker Config auth",
		},
		{
			name: "validate auth (plugin managed, invalid config, ssh auth)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = typeGIT
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_SSH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SshCreds{
						SshCreds: &corev1.SshCredentials{
							PrivateKey: redacted,
							KnownHosts: redacted,
						},
					},
				}
				return request
			},
			expectedErrorCode:   connect.CodeInvalidArgument,
			expectedErrorString: "unexpected REDACTED",
		},
		{
			name: "create with description",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Description = "repository description"
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Annotations = map[string]string{k8sutils.AnnotationDescriptionKey: "repository description"}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with no interval",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Interval = ""
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.SyncPeriod = nil
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with interval",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Interval = "12h"
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.SyncPeriod = &metav1.Duration{Duration: time.Duration(12) * time.Hour}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with url",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Url = "foo"
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.Image = "foo"
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (imgpkg)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = typeImgPkgBundle
				request.Url = "projects.registry.example.com/repo-1/main@sha256:abcd"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						ImgpkgBundle: &kappcorev1.PackageRepositoryImgpkg{
							TagSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
						TagSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (image)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = typeImage
				request.Url = "projects.registry.example.com/repo-1/main@sha256:abcd"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Image: &kappcorev1.PackageRepositoryImage{
							SubPath: "packages",
							TagSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Image: &kappctrlv1alpha1.AppFetchImage{
						URL:     "projects.registry.example.com/repo-1/main@sha256:abcd",
						SubPath: "packages",
						TagSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (git)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = typeGIT
				request.Url = "https://github.com/projects.registry.vmware.com/tce/main"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Git: &kappcorev1.PackageRepositoryGit{
							SubPath: "packages",
							Ref:     "main",
							RefSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
							LfsSkipSmudge: true,
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Git: &kappctrlv1alpha1.AppFetchGit{
						URL:     "https://github.com/projects.registry.vmware.com/tce/main",
						Ref:     "main",
						SubPath: "packages",
						RefSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
						LFSSkipSmudge: true,
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (http)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Type = typeHTTP
				request.Url = "https://projects.registry.vmware.com/tce/main"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Http: &kappcorev1.PackageRepositoryHttp{
							SubPath: "packages",
							Sha256:  "ABC",
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					HTTP: &kappctrlv1alpha1.AppFetchHTTP{
						URL:     "https://projects.registry.vmware.com/tce/main",
						SubPath: "packages",
						SHA256:  "ABC",
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with auth (user managed)",
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: defaultGlobalContext.Namespace, Name: "my-secret"},
					Data:       map[string][]byte{k8scorev1.BasicAuthUsernameKey: []byte("foo"), k8scorev1.BasicAuthPasswordKey: []byte("bar")},
				},
			},
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with auth (plugin managed, basic auth)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_UsernamePassword{
						UsernamePassword: &corev1.UsernamePassword{
							Username: "foo",
							Password: "bar",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{} // the name will be empty as the fake client does not handle generating names
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching newly created secret:%+v", err)
				}
				if !isPluginManaged(defaultRepository(), secret) {
					t.Errorf("annotations and ownership was not properly set: %+v", secret)
				}
				if secret.StringData[k8scorev1.BasicAuthUsernameKey] != "foo" || secret.StringData[k8scorev1.BasicAuthPasswordKey] != "bar" {
					t.Errorf("secret data was not properly constructed: %+v", secret)
				}
			},
		},
		{
			name: "create with auth (plugin managed, bearer auth)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BEARER,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_Header{
						Header: "foo",
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{} // the name will be empty as the fake client does not handle generating names
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching newly created secret:%+v", err)
				}
				if !isPluginManaged(defaultRepository(), secret) {
					t.Errorf("annotations and ownership was not properly set: %+v", secret)
				}
				if secret.Type != k8scorev1.SecretTypeOpaque || secret.StringData[bearerAuthToken] != "foo" {
					t.Errorf("secret data was not properly constructed: %+v", secret)
				}
			},
		},
		{
			name: "create with auth (plugin managed, docker auth)",
			requestCustomizer: func(request *corev1.AddPackageRepositoryRequest) *corev1.AddPackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_DOCKER_CONFIG_JSON,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_DockerCreds{
						DockerCreds: &corev1.DockerCredentials{
							Username: "foo",
							Password: "bar",
							Server:   "localhost",
							Email:    "foo@example.com",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{} // the name will be empty as the fake client does not handle generating names
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching newly created secret:%+v", err)
				}
				if !isPluginManaged(defaultRepository(), secret) {
					t.Errorf("annotations and ownership was not properly set: %+v", secret)
				}
				if secret.Type != k8scorev1.SecretTypeDockerConfigJson || !strings.Contains(secret.StringData[k8scorev1.DockerConfigJsonKey], "foo@example.com") {
					t.Errorf("secret data was not properly constructed: %+v", secret)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			typedClient := typfake.NewSimpleClientset(tc.existingTypedObjects...)
			dynamicClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(
				k8sruntime.NewScheme(),
				map[schema.GroupVersionResource]string{
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
				},
				unstructuredObjects...,
			)

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typedClient).
					WithDynamic(dynamicClient).
					Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			request := tc.requestCustomizer(defaultRequest())
			response, err := s.AddPackageRepository(context.Background(), connect.NewRequest(request))

			// check status
			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got error: %d, want: %d, err: %+v", got, want, err)
			} else if got != 0 {
				if tc.expectedErrorString != "" && !strings.Contains(fmt.Sprint(err), tc.expectedErrorString) {
					t.Fatalf("error without expected string: expected %s, err: %+v", tc.expectedErrorString, err)
				}
				return
			}

			// check ref
			if got, want := response.Msg.GetPackageRepoRef(), tc.expectedRef; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("response mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}

			// check repository
			repository, err := s.getPkgRepository(context.Background(), http.Header{}, tc.expectedRef.Context.Cluster, tc.expectedRef.Context.Namespace, tc.expectedRef.Identifier)
			if err != nil {
				t.Fatalf("unexpected error retrieving repository: %+v", err)
			}
			expectedRepository := tc.repositoryCustomizer(defaultRepository())

			if got, want := repository, expectedRepository; !cmp.Equal(want, got, ignoreUnexported) {
				t.Fatalf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}

			// custom checks
			if tc.customChecks != nil {
				tc.customChecks(t, &s)
			}
		})
	}
}

func TestUpdatePackageRepository(t *testing.T) {
	defaultRef := &corev1.PackageRepositoryReference{
		Plugin:     &pluginDetail,
		Context:    defaultGlobalContext,
		Identifier: "globalrepo",
	}
	defaultRequest := func() *corev1.UpdatePackageRepositoryRequest {
		return &corev1.UpdatePackageRepositoryRequest{
			PackageRepoRef: &corev1.PackageRepositoryReference{
				Plugin:     &pluginDetail,
				Context:    &corev1.Context{Namespace: defaultGlobalContext.Namespace, Cluster: defaultGlobalContext.Cluster},
				Identifier: "globalrepo",
			},
			Url:      "projects.registry.example.com/repo-1/main@sha256:abcd",
			Interval: "24h",
		}
	}
	defaultRepository := func() *packagingv1alpha1.PackageRepository {
		return &packagingv1alpha1.PackageRepository{
			TypeMeta:   defaultTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: defaultGlobalContext.Namespace, UID: "globalrepo"},
			Spec: packagingv1alpha1.PackageRepositorySpec{
				SyncPeriod: &metav1.Duration{Duration: time.Duration(24) * time.Hour},
				Fetch: &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
			Status: packagingv1alpha1.PackageRepositoryStatus{},
		}
	}

	defaultSecret := func(name string, managed bool) *k8scorev1.Secret {
		secret := &k8scorev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: defaultGlobalContext.Namespace,
				Name:      name,
			},
			Type: k8scorev1.SecretTypeOpaque,
			Data: map[string][]byte{},
		}
		if managed {
			secret.ObjectMeta.Annotations = map[string]string{annotationManagedByKey: annotationManagedByValue}
			secret.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: defaultTypeMeta.APIVersion,
					Kind:       defaultTypeMeta.Kind,
					Name:       "globalrepo",
					UID:        "globalrepo",
					Controller: func() *bool { v := true; return &v }(),
				},
			}
		}
		return secret
	}
	basicAuthSecret := func(secret *k8scorev1.Secret, username string, password string) *k8scorev1.Secret {
		secret.Type = k8scorev1.SecretTypeBasicAuth
		secret.Data = map[string][]byte{k8scorev1.BasicAuthUsernameKey: []byte(username), k8scorev1.BasicAuthPasswordKey: []byte(password)}
		return secret
	}
	tokenAuthSecret := func(secret *k8scorev1.Secret, token string) *k8scorev1.Secret {
		secret.Type = k8scorev1.SecretTypeOpaque
		secret.Data = map[string][]byte{bearerAuthToken: []byte(token)}
		return secret
	}
	dockerAuthSecret := func(secret *k8scorev1.Secret, dockerconfig string) *k8scorev1.Secret {
		secret.Type = k8scorev1.SecretTypeDockerConfigJson
		secret.Data = map[string][]byte{k8scorev1.DockerConfigJsonKey: []byte(dockerconfig)}
		return secret
	}

	testCases := []struct {
		name                 string
		existingTypedObjects []k8sruntime.Object
		initialCustomizer    func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository
		requestCustomizer    func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest
		repositoryCustomizer func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository
		expectedErrorCode    connect.Code
		expectedStatusString string
		expectedRef          *corev1.PackageRepositoryReference
		customChecks         func(t *testing.T, s *Server)
	}{
		{
			name: "validate cluster",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.PackageRepoRef.Context = &corev1.Context{Cluster: "other", Namespace: demoGlobalPackagingNamespace}
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate name",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.PackageRepoRef.Identifier = ""
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate url",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Url = ""
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate tls config",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.TlsConfig = &corev1.PackageRepositoryTlsConfig{}
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate auth (data provided with unspecified type)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_UNSPECIFIED,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_UsernamePassword{
						UsernamePassword: &corev1.UsernamePassword{
							Username: "foo",
							Password: "bar",
						},
					},
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "Auth Type is not specified but auth configuration data were provided",
		},
		{
			name: "validate auth (type incompatibility)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_SSH,
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "Auth Type is incompatible",
		},
		{
			name: "validate auth (mode incompatibility)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", false), "foo", "bar"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "management mode cannot be changed",
		},
		{
			name: "validate auth (user managed, invalid secret)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{},
					},
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "the secret name is not provided",
		},
		{
			name: "validate auth (user managed, secret does not exist)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret",
						},
					},
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "not found",
		},
		{
			name: "validate auth (user managed, secret is incompatible)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", false), "foo", "bar"),
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_DOCKER_CONFIG_JSON,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret",
						},
					},
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "the secret does not match",
		},
		{
			name: "validate auth (plugin managed, invalid config, basic auth)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "Missing basic auth",
		},
		{
			name: "validate auth (plugin managed, invalid config, docker)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_DOCKER_CONFIG_JSON,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_DockerCreds{
						DockerCreds: &corev1.DockerCredentials{
							Username: "foo",
							Password: "bar",
							Server:   redacted,
						},
					},
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "unexpected REDACTED",
		},
		{
			name: "validate not found",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.PackageRepoRef.Identifier = "foo"
				return request
			},
			expectedErrorCode: connect.CodeNotFound,
		},
		{
			name: "validate details (invalid type)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.CustomDetail, _ = anypb.New(&corev1.UpdatePackageRepositoryRequest{})
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate details (type mismatch)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Http: &kappcorev1.PackageRepositoryHttp{
							SubPath: "packages",
							Sha256:  "ABC",
						},
					},
				})
				return request
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "validate pending status",
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Status = packagingv1alpha1.PackageRepositoryStatus{
					GenericStatus: kappctrlv1alpha1.GenericStatus{
						Conditions: []kappctrlv1alpha1.Condition{
							{
								Type: kappctrlv1alpha1.Reconciling,
							},
						},
					},
				}
				return repository
			},
			expectedErrorCode:    connect.CodeFailedPrecondition,
			expectedStatusString: "not in a stable state",
		},
		{
			name: "update with new description",
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Annotations = map[string]string{k8sutils.AnnotationDescriptionKey: "initial description"}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Description = "updated description"
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Annotations = map[string]string{k8sutils.AnnotationDescriptionKey: "updated description"}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "update remove description",
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Annotations = map[string]string{k8sutils.AnnotationDescriptionKey: "initial description"}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Description = ""
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Annotations = nil
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "update with no interval",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Interval = ""
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.SyncPeriod = nil
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "updated with new interval",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Interval = "12h"
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.SyncPeriod = &metav1.Duration{Duration: time.Duration(12) * time.Hour}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "updated with new url",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Url = "foo"
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.Image = "foo"
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (imgpkg)",
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Url = "projects.registry.example.com/repo-1/main@sha256:abcd"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						ImgpkgBundle: &kappcorev1.PackageRepositoryImgpkg{
							TagSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
						TagSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (image)",
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Image: &kappctrlv1alpha1.AppFetchImage{
						URL: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Url = "projects.registry.example.com/repo-1/main@sha256:abcd"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Image: &kappcorev1.PackageRepositoryImage{
							SubPath: "packages",
							TagSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Image: &kappctrlv1alpha1.AppFetchImage{
						URL:     "projects.registry.example.com/repo-1/main@sha256:abcd",
						SubPath: "packages",
						TagSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (git)",
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Git: &kappctrlv1alpha1.AppFetchGit{
						URL: "https://github.com/projects.registry.vmware.com/tce/main",
					},
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Url = "https://github.com/projects.registry.vmware.com/tce/main"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Git: &kappcorev1.PackageRepositoryGit{
							SubPath: "packages",
							Ref:     "main",
							RefSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
							LfsSkipSmudge: true,
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Git: &kappctrlv1alpha1.AppFetchGit{
						URL:     "https://github.com/projects.registry.vmware.com/tce/main",
						Ref:     "main",
						SubPath: "packages",
						RefSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
						LFSSkipSmudge: true,
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "create with details (http)",
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					HTTP: &kappctrlv1alpha1.AppFetchHTTP{
						URL: "https://projects.registry.vmware.com/tce/main",
					},
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Url = "https://projects.registry.vmware.com/tce/main"
				request.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Http: &kappcorev1.PackageRepositoryHttp{
							SubPath: "packages",
							Sha256:  "ABC",
						},
					},
				})
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					HTTP: &kappctrlv1alpha1.AppFetchHTTP{
						URL:     "https://projects.registry.vmware.com/tce/main",
						SubPath: "packages",
						SHA256:  "ABC",
					},
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "updated with auth (user managed, added)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", false), "foo", "bar"),
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "updated with auth (user managed, updated)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", false), "foo", "bar"),
				dockerAuthSecret(defaultSecret("my-secret-2", false), "{}"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_DOCKER_CONFIG_JSON,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret-2",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret-2",
				}
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "updated with auth (user managed, removed)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", false), "foo", "bar"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = nil
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "updated with auth (plugin managed, added)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_UsernamePassword{
						UsernamePassword: &corev1.UsernamePassword{
							Username: "foo",
							Password: "bar",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{} // the name will be empty as the fake client does not handle generating names
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching newly created secret:%+v", err)
				}
				if !isPluginManaged(defaultRepository(), secret) {
					t.Errorf("annotations and ownership was not properly set: %+v", secret)
				}
				if secret.Type != k8scorev1.SecretTypeOpaque || secret.StringData[k8scorev1.BasicAuthUsernameKey] != "foo" || secret.StringData[k8scorev1.BasicAuthPasswordKey] != "bar" {
					t.Errorf("secret data was not properly constructed: %+v", secret)
				}
			},
		},
		{
			name: "updated with auth (plugin managed, bearer auth)",
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BEARER,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_Header{
						Header: "foo",
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{} // the name will be empty as the fake client does not handle generating names
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching newly created secret:%+v", err)
				}
				if !isPluginManaged(defaultRepository(), secret) {
					t.Errorf("annotations and ownership was not properly set: %+v", secret)
				}
				if secret.Type != k8scorev1.SecretTypeOpaque || secret.StringData[bearerAuthToken] != "foo" {
					t.Errorf("secret data was not properly constructed: %+v", secret)
				}
			},
		},
		{
			name: "updated with auth (plugin managed, removed)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", true), "foo", "bar"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_UNSPECIFIED,
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = nil
				return repository
			},
			expectedRef: defaultRef,
		},
		{
			name: "updated with auth (plugin managed, update unchanged)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", true), "foo", "bar"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_UsernamePassword{
						UsernamePassword: &corev1.UsernamePassword{
							Username: redacted,
							Password: redacted,
						},
					},
				}
				return request
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "my-secret")
				if err != nil {
					t.Fatalf("error fetching secret:%+v", err)
				}
				if secret.Type != k8scorev1.SecretTypeBasicAuth || string(secret.Data[k8scorev1.BasicAuthUsernameKey]) != "foo" ||
					string(secret.Data[k8scorev1.BasicAuthPasswordKey]) != "bar" || len(secret.StringData) != 0 {
					t.Errorf("secret data not as expected: %+v", secret)
				}
			},
		},
		{
			name: "updated with auth (plugin managed, mixed redacted/updated)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", true), "foo", "bar"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_UsernamePassword{
						UsernamePassword: &corev1.UsernamePassword{
							Username: redacted,
							Password: "bar2",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{} // the name will be empty as the fake client does not handle generating names
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching secret:%+v", err)
				}
				if secret.Type != k8scorev1.SecretTypeOpaque || secret.StringData[k8scorev1.BasicAuthPasswordKey] != "bar2" {
					t.Errorf("secret data not as expected: %+v", secret)
				}
			},
		},
		{
			name: "updated with new auth type (plugin managed, update basic to token)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", true), "foo", "bar"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BEARER,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_Header{
						Header: "zot",
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{} // the name will be empty as the fake client does not handle generating names
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching secret:%+v", err)
				}
				if secret.Type != k8scorev1.SecretTypeOpaque || secret.StringData[bearerAuthToken] != "zot" {
					t.Errorf("secret data not as expected: %+v", secret)
				}
			},
		},
		{
			name: "updated with new auth type (plugin managed, invalid use of redacted)",
			existingTypedObjects: []k8sruntime.Object{
				tokenAuthSecret(defaultSecret("my-secret", true), "zot"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_UsernamePassword{
						UsernamePassword: &corev1.UsernamePassword{
							Username: redacted,
							Password: "bar",
						},
					},
				}
				return request
			},
			expectedErrorCode:    connect.CodeInvalidArgument,
			expectedStatusString: "unexpected REDACTED content",
		},
		{
			name: "updated with new auth type (plugin managed, update basic to ssh, git spec)",
			existingTypedObjects: []k8sruntime.Object{
				basicAuthSecret(defaultSecret("my-secret", true), "foo", "bar"),
			},
			initialCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Git: &kappctrlv1alpha1.AppFetchGit{
						URL: "http://github.com/repo-1/main",
						SecretRef: &kappctrlv1alpha1.AppFetchLocalRef{
							Name: "my-secret",
						},
					},
				}
				return repository
			},
			requestCustomizer: func(request *corev1.UpdatePackageRepositoryRequest) *corev1.UpdatePackageRepositoryRequest {
				request.Url = "http://github.com/repo-1/main"
				request.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_SSH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SshCreds{
						SshCreds: &corev1.SshCredentials{
							PrivateKey: "ssh-key",
						},
					},
				}
				return request
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Git: &kappctrlv1alpha1.AppFetchGit{
						URL:       "http://github.com/repo-1/main",
						SecretRef: &kappctrlv1alpha1.AppFetchLocalRef{}, // the name will be empty as the fake client does not handle generating names
					},
				}
				return repository
			},
			expectedRef: defaultRef,
			customChecks: func(t *testing.T, s *Server) {
				secret, err := s.getSecret(context.Background(), http.Header{}, defaultGlobalContext.Cluster, demoGlobalPackagingNamespace, "")
				if err != nil {
					t.Fatalf("error fetching secret:%+v", err)
				}
				if secret.Type != k8scorev1.SecretTypeOpaque || secret.StringData[k8scorev1.SSHAuthPrivateKey] != "ssh-key" {
					t.Errorf("secret data not as expected: %+v", secret)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repository := defaultRepository()
			if tc.initialCustomizer != nil {
				repository = tc.initialCustomizer(repository)
			}

			var unstructuredObjects []k8sruntime.Object
			for _, obj := range []k8sruntime.Object{repository} {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			typedClient := typfake.NewSimpleClientset(tc.existingTypedObjects...)
			dynamicClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(
				k8sruntime.NewScheme(),
				map[schema.GroupVersionResource]string{
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
				},
				unstructuredObjects...,
			)

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typedClient).
					WithDynamic(dynamicClient).
					Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			// prepare request
			request := defaultRequest()
			if tc.requestCustomizer != nil {
				request = tc.requestCustomizer(request)
			}

			// invoke
			response, err := s.UpdatePackageRepository(context.Background(), connect.NewRequest(request))

			// check status
			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got error: %d, want: %d, err: %+v", got, want, err)
			} else if got != 0 {
				if tc.expectedStatusString != "" && !strings.Contains(fmt.Sprint(err), tc.expectedStatusString) {
					t.Fatalf("error without expected string: expected %s, err: %+v", tc.expectedStatusString, err)
				}
				return
			}

			// check ref
			if got, want := response.Msg.GetPackageRepoRef(), tc.expectedRef; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("response mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}

			// check repository
			pkgrepository, err := s.getPkgRepository(context.Background(), http.Header{}, tc.expectedRef.Context.Cluster, tc.expectedRef.Context.Namespace, tc.expectedRef.Identifier)
			if err != nil {
				t.Fatalf("unexpected error retrieving repository: %+v", err)
			}
			expectedRepository := repository
			if tc.repositoryCustomizer != nil {
				expectedRepository = tc.repositoryCustomizer(repository)
			}

			if got, want := pkgrepository, expectedRepository; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}

			// custom checks
			if tc.customChecks != nil {
				tc.customChecks(t, &s)
			}
		})
	}
}

func TestDeletePackageRepository(t *testing.T) {
	defaultRepository := func() *packagingv1alpha1.PackageRepository {
		return &packagingv1alpha1.PackageRepository{
			TypeMeta:   defaultTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
			Spec: packagingv1alpha1.PackageRepositorySpec{
				Fetch: &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
			Status: packagingv1alpha1.PackageRepositoryStatus{},
		}
	}

	testCases := []struct {
		name              string
		existingObjects   []k8sruntime.Object
		request           *corev1.DeletePackageRepositoryRequest
		expectedErrorCode connect.Code
	}{
		{
			name:            "delete - success",
			existingObjects: []k8sruntime.Object{defaultRepository()},
			request: &corev1.DeletePackageRepositoryRequest{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
			},
		},
		{
			name:            "delete - not found (empty)",
			existingObjects: []k8sruntime.Object{},
			request: &corev1.DeletePackageRepositoryRequest{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
			},
			expectedErrorCode: connect.CodeNotFound,
		},
		{
			name:            "delete - not found (different)",
			existingObjects: []k8sruntime.Object{defaultRepository()},
			request: &corev1.DeletePackageRepositoryRequest{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo2",
				},
			},
			expectedErrorCode: connect.CodeNotFound,
		},
		{
			name: "delete - with user managed secret",
			existingObjects: []k8sruntime.Object{func(r *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				r.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return r
			}(defaultRepository())},
			request: &corev1.DeletePackageRepositoryRequest{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
			},
		},
		{
			name: "delete - with plugin managed secret",
			existingObjects: []k8sruntime.Object{func(r *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				r.ObjectMeta.UID = "globalrepo"
				r.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return r
			}(defaultRepository())},
			request: &corev1.DeletePackageRepositoryRequest{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			dynamicClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(
				k8sruntime.NewScheme(),
				map[schema.GroupVersionResource]string{
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
				},
				unstructuredObjects...,
			)
			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynamicClient).
					Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			_, err := s.DeletePackageRepository(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %d, want: %d, err: %+v", got, want, err)
			}
		})
	}
}

func TestGetPackageRepositoryDetail(t *testing.T) {
	defaultRequest := func() *corev1.GetPackageRepositoryDetailRequest {
		return &corev1.GetPackageRepositoryDetailRequest{
			PackageRepoRef: &corev1.PackageRepositoryReference{
				Plugin:     &pluginDetail,
				Context:    &corev1.Context{Namespace: defaultGlobalContext.Namespace, Cluster: defaultGlobalContext.Cluster},
				Identifier: "globalrepo",
			},
		}
	}
	defaultRepository := func() *packagingv1alpha1.PackageRepository {
		return &packagingv1alpha1.PackageRepository{
			TypeMeta:   defaultTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: defaultGlobalContext.Namespace, UID: "globalrepo"},
			Spec: packagingv1alpha1.PackageRepositorySpec{
				SyncPeriod: &metav1.Duration{Duration: time.Duration(24) * time.Hour},
				Fetch: &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
			Status: packagingv1alpha1.PackageRepositoryStatus{},
		}
	}
	defaultResponse := func() *corev1.GetPackageRepositoryDetailResponse {
		return &corev1.GetPackageRepositoryDetailResponse{
			Detail: &corev1.PackageRepositoryDetail{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Plugin:     &pluginDetail,
					Context:    &corev1.Context{Namespace: defaultGlobalContext.Namespace, Cluster: defaultGlobalContext.Cluster},
					Identifier: "globalrepo",
				},
				Name:            "globalrepo",
				NamespaceScoped: false,
				Type:            typeImgPkgBundle,
				Url:             "projects.registry.example.com/repo-1/main@sha256:abcd",
				Interval:        "24h",
			},
		}
	}

	defaultSecret := func() *k8scorev1.Secret {
		return &k8scorev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   defaultGlobalContext.Namespace,
				Name:        "my-secret",
				Annotations: map[string]string{annotationManagedByKey: annotationManagedByValue},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: defaultTypeMeta.APIVersion,
						Kind:       defaultTypeMeta.Kind,
						Name:       "globalrepo",
						UID:        "globalrepo",
						Controller: func() *bool { v := true; return &v }(),
					},
				},
			},
			Data: map[string][]byte{},
		}
	}

	testCases := []struct {
		name                 string
		existingTypedObjects []k8sruntime.Object
		requestCustomizer    func(request *corev1.GetPackageRepositoryDetailRequest) *corev1.GetPackageRepositoryDetailRequest
		repositoryCustomizer func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository
		responseCustomizer   func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse
		expectedErrorCode    connect.Code
	}{
		{
			name: "not found",
			requestCustomizer: func(request *corev1.GetPackageRepositoryDetailRequest) *corev1.GetPackageRepositoryDetailRequest {
				request.PackageRepoRef.Identifier = "foo"
				return request
			},
			expectedErrorCode: connect.CodeNotFound,
		},
		{
			name: "check ref",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.ObjectMeta.Name = "foo"
				repository.Name = "foo"
				return repository
			},
			requestCustomizer: func(request *corev1.GetPackageRepositoryDetailRequest) *corev1.GetPackageRepositoryDetailRequest {
				request.PackageRepoRef.Identifier = "foo"
				return request
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.PackageRepoRef.Identifier = "foo"
				response.Detail.Name = "foo"
				return response
			},
		},
		{
			name: "check name",
		},
		{
			name: "check global scope",
		},
		{
			name: "check namespace scoped",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.ObjectMeta.Namespace = "privatens"
				return repository
			},
			requestCustomizer: func(request *corev1.GetPackageRepositoryDetailRequest) *corev1.GetPackageRepositoryDetailRequest {
				request.PackageRepoRef.Context.Namespace = "privatens"
				return request
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.PackageRepoRef.Context.Namespace = "privatens"
				response.Detail.NamespaceScoped = true
				return response
			},
		},
		{
			name: "check url",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.Image = "foo"
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Url = "foo"
				return response
			},
		},
		{
			name: "check description",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Annotations = map[string]string{k8sutils.AnnotationDescriptionKey: "repository description"}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Description = "repository description"
				return response
			},
		},
		{
			name: "check interval (none)",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.SyncPeriod = nil
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Interval = ""
				return response
			},
		},
		{
			name: "check interval (set)",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.SyncPeriod = &metav1.Duration{Duration: time.Duration(12) * time.Hour}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Interval = "12h"
				return response
			},
		},
		{
			name: "check imgpkg type",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
						TagSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
					},
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Type = typeImgPkgBundle
				response.Detail.Url = "projects.registry.example.com/repo-1/main@sha256:abcd"
				response.Detail.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						ImgpkgBundle: &kappcorev1.PackageRepositoryImgpkg{
							TagSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
						},
					},
				})
				return response
			},
		},
		{
			name: "check image type",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Image: &kappctrlv1alpha1.AppFetchImage{
						URL:     "projects.registry.example.com/repo-1/main@sha256:abcd",
						SubPath: "packages",
						TagSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
					},
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Type = typeImage
				response.Detail.Url = "projects.registry.example.com/repo-1/main@sha256:abcd"
				response.Detail.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Image: &kappcorev1.PackageRepositoryImage{
							SubPath: "packages",
							TagSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
						},
					},
				})
				return response
			},
		},
		{
			name: "check git type",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Git: &kappctrlv1alpha1.AppFetchGit{
						URL:     "https://github.com/projects.registry.vmware.com/tce/main",
						Ref:     "main",
						SubPath: "packages",
						RefSelection: &vendirversions.VersionSelection{
							Semver: &vendirversions.VersionSelectionSemver{
								Constraints: ">0.10.0 <0.11.0",
								Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
									Identifiers: []string{"beta", "rc"},
								},
							},
						},
						LFSSkipSmudge: true,
					},
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Type = typeGIT
				response.Detail.Url = "https://github.com/projects.registry.vmware.com/tce/main"
				response.Detail.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Git: &kappcorev1.PackageRepositoryGit{
							SubPath: "packages",
							Ref:     "main",
							RefSelection: &kappcorev1.VersionSelection{
								Semver: &kappcorev1.VersionSelectionSemver{
									Constraints: ">0.10.0 <0.11.0",
									Prereleases: &kappcorev1.VersionSelectionSemverPrereleases{
										Identifiers: []string{"beta", "rc"},
									},
								},
							},
							LfsSkipSmudge: true,
						},
					},
				})
				return response
			},
		},
		{
			name: "check http type",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					HTTP: &kappctrlv1alpha1.AppFetchHTTP{
						URL:     "https://projects.registry.vmware.com/tce/main",
						SubPath: "packages",
						SHA256:  "ABC",
					},
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Type = typeHTTP
				response.Detail.Url = "https://projects.registry.vmware.com/tce/main"
				response.Detail.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Http: &kappcorev1.PackageRepositoryHttp{
							SubPath: "packages",
							Sha256:  "ABC",
						},
					},
				})
				return response
			},
		},
		{
			name: "check inline type",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch = &packagingv1alpha1.PackageRepositoryFetch{
					Inline: &kappctrlv1alpha1.AppFetchInline{
						Paths: map[string]string{
							"dir/file.ext": "foo",
						},
						PathsFrom: []kappctrlv1alpha1.AppFetchInlineSource{
							{
								SecretRef: &kappctrlv1alpha1.AppFetchInlineSourceRef{Name: "my-secret", DirectoryPath: "foo"},
							},
							{
								SecretRef:    &kappctrlv1alpha1.AppFetchInlineSourceRef{Name: "my-secret", DirectoryPath: "foo"},
								ConfigMapRef: &kappctrlv1alpha1.AppFetchInlineSourceRef{Name: "my-secret", DirectoryPath: "bar"},
							},
						},
					},
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Type = typeInline
				response.Detail.Url = ""
				response.Detail.CustomDetail, _ = anypb.New(&kappcorev1.KappControllerPackageRepositoryCustomDetail{
					Fetch: &kappcorev1.PackageRepositoryFetch{
						Inline: &kappcorev1.PackageRepositoryInline{
							Paths: map[string]string{
								"dir/file.ext": "foo",
							},
							PathsFrom: []*kappcorev1.PackageRepositoryInline_Source{
								{
									SecretRef: &kappcorev1.PackageRepositoryInline_SourceRef{Name: "my-secret", DirectoryPath: "foo"},
								},
								{
									SecretRef:    &kappcorev1.PackageRepositoryInline_SourceRef{Name: "my-secret", DirectoryPath: "foo"},
									ConfigMapRef: &kappcorev1.PackageRepositoryInline_SourceRef{Name: "my-secret", DirectoryPath: "bar"},
								},
							},
						},
					},
				})
				return response
			},
		},
		{
			name: "check auth - missing secret",
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			expectedErrorCode: connect.CodeNotFound,
		},
		{
			name: "check auth - user managed secret",
			existingTypedObjects: []k8sruntime.Object{
				&k8scorev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: defaultGlobalContext.Namespace, Name: "my-secret"},
					Data:       map[string][]byte{k8scorev1.BasicAuthUsernameKey: []byte("foo"), k8scorev1.BasicAuthPasswordKey: []byte("bar")},
				},
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SecretRef{
						SecretRef: &corev1.SecretKeyReference{
							Name: "my-secret",
						},
					},
				}
				return response
			},
		},
		{
			name: "check auth - plugin managed secret - basic auth",
			existingTypedObjects: []k8sruntime.Object{
				func() *k8scorev1.Secret {
					s := defaultSecret()
					s.Data[k8scorev1.BasicAuthUsernameKey] = []byte("foo")
					s.Data[k8scorev1.BasicAuthPasswordKey] = []byte("bar")
					return s
				}(),
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BASIC_AUTH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_UsernamePassword{
						UsernamePassword: &corev1.UsernamePassword{
							Username: redacted,
							Password: redacted,
						},
					},
				}
				return response
			},
		},
		{
			name: "check auth - plugin managed secret - ssh auth",
			existingTypedObjects: []k8sruntime.Object{
				func() *k8scorev1.Secret {
					s := defaultSecret()
					s.Data[k8scorev1.SSHAuthPrivateKey] = []byte("foo")
					return s
				}(),
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_SSH,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_SshCreds{
						SshCreds: &corev1.SshCredentials{
							PrivateKey: redacted,
							KnownHosts: redacted,
						},
					},
				}
				return response
			},
		},
		{
			name: "check auth - plugin managed secret - bearer auth",
			existingTypedObjects: []k8sruntime.Object{
				func() *k8scorev1.Secret {
					s := defaultSecret()
					s.Data[bearerAuthToken] = []byte("foo")
					return s
				}(),
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_BEARER,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_Header{
						Header: redacted,
					},
				}
				return response
			},
		},
		{
			name: "check auth - plugin managed secret - docker auth",
			existingTypedObjects: []k8sruntime.Object{
				func() *k8scorev1.Secret {
					s := defaultSecret()
					s.Data[k8scorev1.DockerConfigJsonKey] = []byte(`{ "auths": { "localhost": { "username": "foo", "password": "bar", "email": "foo@example.com" }}}`)
					return s
				}(),
			},
			repositoryCustomizer: func(repository *packagingv1alpha1.PackageRepository) *packagingv1alpha1.PackageRepository {
				repository.Spec.Fetch.ImgpkgBundle.SecretRef = &kappctrlv1alpha1.AppFetchLocalRef{
					Name: "my-secret",
				}
				return repository
			},
			responseCustomizer: func(response *corev1.GetPackageRepositoryDetailResponse) *corev1.GetPackageRepositoryDetailResponse {
				response.Detail.Auth = &corev1.PackageRepositoryAuth{
					Type: corev1.PackageRepositoryAuth_PACKAGE_REPOSITORY_AUTH_TYPE_DOCKER_CONFIG_JSON,
					PackageRepoAuthOneOf: &corev1.PackageRepositoryAuth_DockerCreds{
						DockerCreds: &corev1.DockerCredentials{
							Server:   redacted,
							Username: redacted,
							Password: redacted,
							Email:    redacted,
						},
					},
				}
				return response
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repository := defaultRepository()
			if tc.repositoryCustomizer != nil {
				repository = tc.repositoryCustomizer(repository)
			}

			var unstructuredObjects []k8sruntime.Object
			for _, obj := range []k8sruntime.Object{repository} {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			typedClient := typfake.NewSimpleClientset(tc.existingTypedObjects...)
			dynamicClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(
				k8sruntime.NewScheme(),
				map[schema.GroupVersionResource]string{
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
				},
				unstructuredObjects...,
			)

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typedClient).
					WithDynamic(dynamicClient).
					Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			// invocation
			request := defaultRequest()
			if tc.requestCustomizer != nil {
				request = tc.requestCustomizer(request)
			}

			response, err := s.GetPackageRepositoryDetail(context.Background(), connect.NewRequest(request))

			// checks
			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got error: %d, want: %d, err: %+v", got, want, err)
			} else if got != 0 {
				return
			}

			if got, want := request.PackageRepoRef, response.Msg.Detail.PackageRepoRef; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("ref mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}

			expectedResponse := defaultResponse()
			if tc.responseCustomizer != nil {
				expectedResponse = tc.responseCustomizer(expectedResponse)
			}

			if got, want := response.Msg, expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

func TestGetPackageRepositorySummaries(t *testing.T) {
	testCases := []struct {
		name             string
		existingObjects  []k8sruntime.Object
		expectedResponse *corev1.PackageRepositorySummary
	}{
		{
			name: "test namespace scope for private ns",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "nsrepo", Namespace: "privatens"},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    &corev1.Context{Namespace: "privatens", Cluster: defaultContext.Cluster},
					Plugin:     &pluginDetail,
					Identifier: "nsrepo",
				},
				Name:            "nsrepo",
				NamespaceScoped: true,
				Type:            typeImgPkgBundle,
				Url:             "projects.registry.example.com/repo-1/main@sha256:abcd",
				RequiresAuth:    false,
			},
		},
		{
			name: "test namespace scope for global ns",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:            "globalrepo",
				NamespaceScoped: false,
				Type:            typeImgPkgBundle,
				Url:             "projects.registry.example.com/repo-1/main@sha256:abcd",
				RequiresAuth:    false,
			},
		},
		{
			name: "test imgpkg translation",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Type:         typeImgPkgBundle,
				Url:          "projects.registry.example.com/repo-1/main@sha256:abcd",
				RequiresAuth: false,
			},
		},
		{
			name: "test image translation",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							Image: &kappctrlv1alpha1.AppFetchImage{
								URL: "projects.registry.example.com/repo-1/main@sha256:abcd",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Type:         typeImage,
				Url:          "projects.registry.example.com/repo-1/main@sha256:abcd",
				RequiresAuth: false,
			},
		},
		{
			name: "test git translation",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							Git: &kappctrlv1alpha1.AppFetchGit{
								URL: "https://github.com/projects.registry.vmware.com/tce/main",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Type:         typeGIT,
				Url:          "https://github.com/projects.registry.vmware.com/tce/main",
				RequiresAuth: false,
			},
		},
		{
			name: "test http translation",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							HTTP: &kappctrlv1alpha1.AppFetchHTTP{
								URL: "https://projects.registry.vmware.com/tce/main",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Type:         typeHTTP,
				Url:          "https://projects.registry.vmware.com/tce/main",
				RequiresAuth: false,
			},
		},
		{
			name: "test inline translation",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							Inline: &kappctrlv1alpha1.AppFetchInline{},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Type:         typeInline,
				RequiresAuth: false,
			},
		},
		{
			name: "test with details",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
								TagSelection: &vendirversions.VersionSelection{
									Semver: &vendirversions.VersionSelectionSemver{
										Constraints: ">0.10.0 <0.11.0",
										Prereleases: &vendirversions.VersionSelectionSemverPrereleases{
											Identifiers: []string{"beta", "rc"},
										},
									},
								},
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Type:         typeImgPkgBundle,
				Url:          "projects.registry.example.com/repo-1/main@sha256:abcd",
				RequiresAuth: false,
			},
		},
		{
			name: "test with auth",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
								SecretRef: &kappctrlv1alpha1.AppFetchLocalRef{
									Name: "my-secret",
								},
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Type:         typeImgPkgBundle,
				Url:          "projects.registry.example.com/repo-1/main@sha256:abcd",
				RequiresAuth: true,
			},
		},
		{
			name: "test with description",
			existingObjects: []k8sruntime.Object{
				&packagingv1alpha1.PackageRepository{
					TypeMeta:   defaultTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace, Annotations: map[string]string{k8sutils.AnnotationDescriptionKey: "repository summary description"}},
					Spec: packagingv1alpha1.PackageRepositorySpec{
						Fetch: &packagingv1alpha1.PackageRepositoryFetch{
							ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
								Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
							},
						},
					},
					Status: packagingv1alpha1.PackageRepositoryStatus{},
				},
			},
			expectedResponse: &corev1.PackageRepositorySummary{
				PackageRepoRef: &corev1.PackageRepositoryReference{
					Context:    defaultGlobalContext,
					Plugin:     &pluginDetail,
					Identifier: "globalrepo",
				},
				Name:         "globalrepo",
				Description:  "repository summary description",
				Type:         typeImgPkgBundle,
				Url:          "projects.registry.example.com/repo-1/main@sha256:abcd",
				RequiresAuth: false,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
						},
						unstructuredObjects...,
					)).Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			// query repositories
			response, err := s.GetPackageRepositorySummaries(context.Background(), connect.NewRequest(&corev1.GetPackageRepositorySummariesRequest{
				Context: &corev1.Context{Namespace: ""},
			}))
			if err != nil {
				t.Fatalf("received unexpected error: %+v", err)
			}

			// fail fast
			if len(response.Msg.GetPackageRepositorySummaries()) != 1 {
				t.Fatalf("mistmatch on number of summaries received, expected 1 but got %d", len(response.Msg.PackageRepositorySummaries))
			}
			if got, want := response.Msg.PackageRepositorySummaries[0], tc.expectedResponse; !cmp.Equal(got, want, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

type ClientReaction struct {
	verb     string
	resource string
	reaction k8stesting.ReactionFunc
}

func TestGetPackageRepositorySummariesNamespaces(t *testing.T) {
	testCases := []struct {
		name               string
		request            *corev1.GetPackageRepositorySummariesRequest
		existingRepos      []k8sruntime.Object
		existingNamespaces []*k8scorev1.Namespace
		expectedErrorCode  connect.Code
		expectedResponse   *corev1.GetPackageRepositorySummariesResponse
		reactors           []*ClientReaction
	}{
		{
			name: "returns actual accessible package summaries when namespace not specified and no cluster level access",
			request: &corev1.GetPackageRepositorySummariesRequest{
				Context: &corev1.Context{Cluster: "default"},
			},
			existingNamespaces: []*k8scorev1.Namespace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "ns-accessible",
					},
					Status: k8scorev1.NamespaceStatus{
						Phase: k8scorev1.NamespaceActive,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "ns-inaccessible",
					},
					Status: k8scorev1.NamespaceStatus{
						Phase: k8scorev1.NamespaceActive,
					},
				},
			},
			reactors: []*ClientReaction{
				{
					verb:     "list",
					resource: "packagerepositories",
					reaction: func(action k8stesting.Action) (handled bool, ret k8sruntime.Object, err error) {
						switch action.GetNamespace() {
						// Forbidden cluster-wide listing and a specific namespace
						case "":
							return true, nil, k8sErrors.NewForbidden(authorizationv1.Resource("PackageRepository"), "", errors.New("bang"))
						case "ns-inaccessible":
							return true, nil, k8sErrors.NewForbidden(authorizationv1.Resource("PackageRepository"), "", errors.New("bang"))
						case "ns-accessible":
							return true, &packagingv1alpha1.PackageRepositoryList{
								Items: []packagingv1alpha1.PackageRepository{
									{
										TypeMeta:   defaultTypeMeta,
										ObjectMeta: metav1.ObjectMeta{Name: "repo-accessible-1", Namespace: "ns-accessible"},
										Spec: packagingv1alpha1.PackageRepositorySpec{
											Fetch: &packagingv1alpha1.PackageRepositoryFetch{
												ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
													Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
												},
											},
										},
										Status: packagingv1alpha1.PackageRepositoryStatus{},
									},
								},
							}, nil
						default:
							return true, &packagingv1alpha1.PackageRepositoryList{}, nil
						}
					},
				},
			},
			expectedResponse: &corev1.GetPackageRepositorySummariesResponse{
				PackageRepositorySummaries: []*corev1.PackageRepositorySummary{
					{
						PackageRepoRef: &corev1.PackageRepositoryReference{
							Context:    &corev1.Context{Cluster: defaultContext.Cluster, Namespace: "ns-accessible"},
							Plugin:     &pluginDetail,
							Identifier: "repo-accessible-1",
						},
						Name:            "repo-accessible-1",
						NamespaceScoped: true,
						Type:            "imgpkgBundle",
						Url:             "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			if tc.existingRepos != nil {
				for _, repo := range tc.existingRepos {
					unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(repo)
					unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
				}
			}

			var typedObjects []k8sruntime.Object
			if tc.existingNamespaces != nil {
				for _, ns := range tc.existingNamespaces {
					typedObjects = append(typedObjects, ns)
				}
			}

			scheme := k8sruntime.NewScheme()
			err := packagingv1alpha1.AddToScheme(scheme)
			if err != nil {
				log.Fatalf("%s", err)
			}
			err = authorizationv1.AddToScheme(scheme)
			if err != nil {
				log.Fatalf("%s", err)
			}

			dynClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(
				scheme,
				map[schema.GroupVersionResource]string{
					{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
				},
				unstructuredObjects...,
			)
			for _, reaction := range tc.reactors {
				dynClient.PrependReactor(reaction.verb, reaction.resource, reaction.reaction)
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typfake.NewSimpleClientset(typedObjects...)).
					WithDynamic(dynClient).
					Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			response, err := s.GetPackageRepositorySummaries(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %+v, want: %+v, err: %+v", got, want, err)
			}

			// We don't need to check anything else for non-OK codes.
			if tc.expectedErrorCode != 0 {
				return
			}

			if got, want := response.Msg, tc.expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

func TestGetPackageRepositorySummariesFiltering(t *testing.T) {
	repositories := []k8sruntime.Object{
		&packagingv1alpha1.PackageRepository{
			TypeMeta:   defaultTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
			Spec: packagingv1alpha1.PackageRepositorySpec{
				Fetch: &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
			Status: packagingv1alpha1.PackageRepositoryStatus{},
		},
		&packagingv1alpha1.PackageRepository{
			TypeMeta:   defaultTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Name: "nsrepo", Namespace: "privatens"},
			Spec: packagingv1alpha1.PackageRepositorySpec{
				Fetch: &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
			Status: packagingv1alpha1.PackageRepositoryStatus{},
		},
	}

	testCases := []struct {
		name             string
		request          *corev1.GetPackageRepositorySummariesRequest
		existingObjects  []k8sruntime.Object
		expectedResponse []metav1.ObjectMeta
	}{
		{
			name: "returns repositories from other namespace",
			request: &corev1.GetPackageRepositorySummariesRequest{
				Context: &corev1.Context{Namespace: "default"},
			},
			existingObjects: repositories,
			expectedResponse: []metav1.ObjectMeta{
				{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
			},
		},
		{
			name: "returns repositories from given namespace",
			request: &corev1.GetPackageRepositorySummariesRequest{
				Context: &corev1.Context{Namespace: "privatens"},
			},
			existingObjects: repositories,
			expectedResponse: []metav1.ObjectMeta{
				{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
				{Name: "nsrepo", Namespace: "privatens"},
			},
		},
		{
			name: "returns repositories from global namespace",
			request: &corev1.GetPackageRepositorySummariesRequest{
				Context: &corev1.Context{Namespace: demoGlobalPackagingNamespace},
			},
			existingObjects: repositories,
			expectedResponse: []metav1.ObjectMeta{
				{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
			},
		},
		{
			name: "returns repositories from all namespaces",
			request: &corev1.GetPackageRepositorySummariesRequest{
				Context: &corev1.Context{Namespace: ""},
			},
			existingObjects: repositories,
			expectedResponse: []metav1.ObjectMeta{
				{Name: "globalrepo", Namespace: demoGlobalPackagingNamespace},
				{Name: "nsrepo", Namespace: "privatens"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var unstructuredObjects []k8sruntime.Object
			for _, obj := range tc.existingObjects {
				unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(obj)
				unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})
			}

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
						},
						unstructuredObjects...,
					)).Build(),
			}

			// should not happen
			response, err := s.GetPackageRepositorySummaries(context.Background(), connect.NewRequest(tc.request))
			if err != nil {
				t.Fatalf("received unexpected error: %+v", err)
			}

			// fail fast
			if len(response.Msg.PackageRepositorySummaries) != len(tc.expectedResponse) {
				t.Fatalf("mistmatch on number of summaries received, expected %d but got %d", len(tc.expectedResponse), len(response.Msg.PackageRepositorySummaries))
			}

			// sort response
			sort.Slice(response.Msg.PackageRepositorySummaries, func(i, j int) bool {
				refi := response.Msg.PackageRepositorySummaries[i].GetPackageRepoRef()
				refj := response.Msg.PackageRepositorySummaries[j].GetPackageRepoRef()
				return refi.GetIdentifier() < refj.GetIdentifier()
			})

			for i := 0; i < len(tc.expectedResponse); i++ {
				expected := tc.expectedResponse[i]
				receivedRef := response.Msg.PackageRepositorySummaries[i].GetPackageRepoRef()
				if expected.Name != receivedRef.GetIdentifier() {
					t.Fatalf("expected to received repository named %s but received name %s", expected.Name, receivedRef.GetIdentifier())
				}
				if expected.Namespace != receivedRef.GetContext().GetNamespace() {
					t.Fatalf("expected to received repository in namespace %s but received namespace %s", expected.Namespace, receivedRef.GetContext().GetNamespace())
				}
			}
		})
	}
}

func TestGetPackageRepositoryStatus(t *testing.T) {
	factory := func(status kappctrlv1alpha1.GenericStatus) *packagingv1alpha1.PackageRepository {
		return &packagingv1alpha1.PackageRepository{
			TypeMeta:   defaultTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Name: "nsrepo", Namespace: "privatens"},
			Spec: packagingv1alpha1.PackageRepositorySpec{
				Fetch: &packagingv1alpha1.PackageRepositoryFetch{
					ImgpkgBundle: &kappctrlv1alpha1.AppFetchImgpkgBundle{
						Image: "projects.registry.example.com/repo-1/main@sha256:abcd",
					},
				},
			},
			Status: packagingv1alpha1.PackageRepositoryStatus{
				GenericStatus: status,
			},
		}
	}

	testCases := []struct {
		name             string
		existingStatus   kappctrlv1alpha1.GenericStatus
		expectedResponse *corev1.PackageRepositoryStatus
	}{
		{
			name: "default success case",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type:    kappctrlv1alpha1.ReconcileSucceeded,
						Message: "Succeeded",
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Ready:      true,
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_SUCCESS,
				UserReason: "Succeeded",
			},
		},
		{
			name: "reconciling, server message",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type:    kappctrlv1alpha1.Reconciling,
						Message: "Fetching in progress",
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_PENDING,
				UserReason: "Fetching in progress",
			},
		},
		{
			name: "reconciling, default message",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type: kappctrlv1alpha1.Reconciling,
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_PENDING,
				UserReason: "Reconciling",
			},
		},
		{
			name: "deleting, server message",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type:    kappctrlv1alpha1.Deleting,
						Message: "Terminating",
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_PENDING,
				UserReason: "Terminating",
			},
		},
		{
			name: "deleting, default message",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type: kappctrlv1alpha1.Deleting,
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_PENDING,
				UserReason: "Deleting",
			},
		},
		{
			name: "reconciliation failure",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type:    kappctrlv1alpha1.ReconcileFailed,
						Message: "fetch failure",
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_FAILED,
				UserReason: "fetch failure",
			},
		},
		{
			name: "reconciliation failure, extra error message",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type:    kappctrlv1alpha1.ReconcileFailed,
						Message: "see .status.usefulErrorMessage for detailed error message",
					},
				},
				UsefulErrorMessage: "fetch failed connecting to registry",
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_FAILED,
				UserReason: "fetch failed connecting to registry",
			},
		},
		{
			name: "deletion failure",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type:    kappctrlv1alpha1.DeleteFailed,
						Message: "failed termination",
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_FAILED,
				UserReason: "failed termination",
			},
		},
		{
			name: "unknown type",
			existingStatus: kappctrlv1alpha1.GenericStatus{
				Conditions: []kappctrlv1alpha1.Condition{
					{
						Type:    "unknown",
						Message: "unexpected failure",
					},
				},
			},
			expectedResponse: &corev1.PackageRepositoryStatus{
				Reason:     corev1.PackageRepositoryStatus_STATUS_REASON_UNSPECIFIED,
				UserReason: "unexpected failure",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var repository = factory(tc.existingStatus)

			var unstructuredObjects []k8sruntime.Object
			unstructuredContent, _ := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(repository)
			unstructuredObjects = append(unstructuredObjects, &unstructured.Unstructured{Object: unstructuredContent})

			s := Server{
				pluginConfig: defaultPluginConfig,
				clientGetter: clientgetter.NewBuilder().
					WithDynamic(dynfake.NewSimpleDynamicClientWithCustomListKinds(
						k8sruntime.NewScheme(),
						map[schema.GroupVersionResource]string{
							{Group: packagingv1alpha1.SchemeGroupVersion.Group, Version: packagingv1alpha1.SchemeGroupVersion.Version, Resource: pkgRepositoriesResource}: pkgRepositoryResource + "List",
						},
						unstructuredObjects...,
					)).Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			// should not happen
			response, err := s.GetPackageRepositorySummaries(context.Background(), connect.NewRequest(&corev1.GetPackageRepositorySummariesRequest{
				Context: &corev1.Context{Namespace: ""},
			}))
			if err != nil {
				t.Fatalf("received unexpected error: %+v", err)
			}

			// fail fast
			if len(response.Msg.GetPackageRepositorySummaries()) != 1 {
				t.Fatalf("mistmatch on number of summaries received, expected 1 but got %d", len(response.Msg.PackageRepositorySummaries))
			}
			if got, want := response.Msg.PackageRepositorySummaries[0].Status, tc.expectedResponse; !cmp.Equal(got, want, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

// plugin

func TestParsePluginConfig(t *testing.T) {
	testCases := []struct {
		name                 string
		pluginYAMLConf       []byte
		expectedPluginConfig *kappControllerPluginParsedConfig
		expectedErrorStr     string
	}{
		{
			name:                 "non existing plugin-config file",
			pluginYAMLConf:       nil,
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "no such file or directory",
		},
		{
			name: "no config options are set",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      `),
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "",
		},
		{
			name: "defaultUpgradePolicy: major",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultUpgradePolicy: major
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               pkgutils.UpgradePolicyMajor,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultUpgradePolicy: minor",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultUpgradePolicy: minor
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               pkgutils.UpgradePolicyMinor,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultUpgradePolicy: patch",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultUpgradePolicy: patch
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               pkgutils.UpgradePolicyPatch,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultUpgradePolicy: none",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultUpgradePolicy: none
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               pkgutils.UpgradePolicyNone,
				defaultPrereleasesVersionSelection: defaultPluginConfig.defaultPrereleasesVersionSelection,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultPrereleasesVersionSelection: nil",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: nil,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultPrereleasesVersionSelection: null",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: null
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: nil,
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultPrereleasesVersionSelection: []",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: []
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: []string{},
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultPrereleasesVersionSelection: ['foo']",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: ["foo"]
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: []string{"foo"},
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultPrereleasesVersionSelection: ['foo','bar']",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: ["foo","bar"]
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: []string{"foo", "bar"},
				defaultAllowDowngrades:             defaultPluginConfig.defaultAllowDowngrades,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultAllowDowngrades: false",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: false
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: []string{"foo", "bar"},
				defaultAllowDowngrades:             false,
			},
			expectedErrorStr: "",
		},
		{
			name: "defaultAllowDowngrades: true",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: true
        `),
			expectedPluginConfig: &kappControllerPluginParsedConfig{
				defaultUpgradePolicy:               defaultPluginConfig.defaultUpgradePolicy,
				defaultPrereleasesVersionSelection: []string{"foo", "bar"},
				defaultAllowDowngrades:             true,
			},
			expectedErrorStr: "",
		},
		{
			name: "invalid defaultUpgradePolicy",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultUpgradePolicy: foo
      `),
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "unsupported upgrade policy: [foo]",
		},
		{
			name: "invalid defaultUpgradePolicy",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultUpgradePolicy: 10.09
      `),
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "json: cannot unmarshal",
		},
		{
			name: "invalid defaultPrereleasesVersionSelection",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: trueish
      `),
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "json: cannot unmarshal",
		},
		{
			name: "invalid defaultPrereleasesVersionSelection",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultPrereleasesVersionSelection: 10.09
      `),
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "json: cannot unmarshal",
		},
		{
			name: "invalid defaultAllowDowngrades",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultAllowDowngrades: trueish
      `),
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "json: cannot unmarshal",
		},
		{
			name: "invalid defaultPrereleasesVersionSelection",
			pluginYAMLConf: []byte(`
kappController:
  packages:
    v1alpha1:
      defaultAllowDowngrades: 10.09
      `),
			expectedPluginConfig: defaultPluginConfig,
			expectedErrorStr:     "json: cannot unmarshal",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// TODO(agamez): env vars and file paths should be handled properly for Windows operating system
			if runtime.GOOS == "windows" {
				t.Skip("Skipping in a Windows OS")
			}
			filename := ""
			if tc.pluginYAMLConf != nil {
				pluginJSONConf, err := yaml.YAMLToJSON(tc.pluginYAMLConf)
				if err != nil {
					log.Fatalf("%s", err)
				}
				f, err := os.CreateTemp(".", "plugin_json_conf")
				if err != nil {
					log.Fatalf("%s", err)
				}
				defer os.Remove(f.Name()) // clean up
				if _, err := f.Write(pluginJSONConf); err != nil {
					log.Fatalf("%s", err)
				}
				if err := f.Close(); err != nil {
					log.Fatalf("%s", err)
				}
				filename = f.Name()
			}
			defaultUpgradePolicy, goterr := parsePluginConfig(filename)
			if goterr != nil && !strings.Contains(goterr.Error(), tc.expectedErrorStr) {
				t.Errorf("err got %q, want to find %q", goterr.Error(), tc.expectedErrorStr)
			}
			if got, want := defaultUpgradePolicy, tc.expectedPluginConfig; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}

		})
	}
}

func TestGetPackageRepositoryPermissions(t *testing.T) {

	testCases := []struct {
		name              string
		request           *corev1.GetPackageRepositoryPermissionsRequest
		expectedErrorCode connect.Code
		expectedResponse  *corev1.GetPackageRepositoryPermissionsResponse
		reactors          []*ClientReaction
	}{
		{
			name: "returns permissions for global package repositories",
			request: &corev1.GetPackageRepositoryPermissionsRequest{
				Context: &corev1.Context{Cluster: defaultContext.Cluster},
			},
			reactors: []*ClientReaction{
				{
					verb:     "create",
					resource: "selfsubjectaccessreviews",
					reaction: func(action k8stesting.Action) (handled bool, ret k8sruntime.Object, err error) {
						createAction := action.(k8stesting.CreateActionImpl)
						accessReview := createAction.Object.(*authorizationv1.SelfSubjectAccessReview)
						if accessReview.Spec.ResourceAttributes.Namespace != fallbackGlobalPackagingNamespace {
							return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: false}}, nil
						}
						switch accessReview.Spec.ResourceAttributes.Verb {
						case "list", "delete":
							return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
						default:
							return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: false}}, nil
						}
					},
				},
			},
			expectedResponse: &corev1.GetPackageRepositoryPermissionsResponse{
				Permissions: []*corev1.PackageRepositoriesPermissions{
					{
						Plugin: GetPluginDetail(),
						Global: map[string]bool{
							"create": false,
							"delete": true,
							"get":    false,
							"list":   true,
							"update": false,
							"watch":  false,
						},
						Namespace: nil,
					},
				},
			},
		},
		{
			name:    "returns local permissions when no cluster specified",
			request: &corev1.GetPackageRepositoryPermissionsRequest{},
			reactors: []*ClientReaction{
				{
					verb:     "create",
					resource: "selfsubjectaccessreviews",
					reaction: func(action k8stesting.Action) (handled bool, ret k8sruntime.Object, err error) {
						return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
					},
				},
			},
			expectedResponse: &corev1.GetPackageRepositoryPermissionsResponse{
				Permissions: []*corev1.PackageRepositoriesPermissions{
					{
						Plugin: GetPluginDetail(),
						Global: map[string]bool{
							"create": true,
							"delete": true,
							"get":    true,
							"list":   true,
							"update": true,
							"watch":  true,
						},
						Namespace: nil,
					},
				},
			},
		},
		{
			name: "fails when namespace is specified but not the cluster",
			request: &corev1.GetPackageRepositoryPermissionsRequest{
				Context: &corev1.Context{Namespace: "my-ns"},
			},
			expectedErrorCode: connect.CodeInvalidArgument,
		},
		{
			name: "returns permissions for namespaced package repositories",
			request: &corev1.GetPackageRepositoryPermissionsRequest{
				Context: &corev1.Context{Cluster: defaultContext.Cluster, Namespace: "my-ns"},
			},
			reactors: []*ClientReaction{
				{
					verb:     "create",
					resource: "selfsubjectaccessreviews",
					reaction: func(action k8stesting.Action) (handled bool, ret k8sruntime.Object, err error) {
						createAction := action.(k8stesting.CreateActionImpl)
						accessReview := createAction.Object.(*authorizationv1.SelfSubjectAccessReview)
						if accessReview.Spec.ResourceAttributes.Namespace == fallbackGlobalPackagingNamespace {
							return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
						}
						switch accessReview.Spec.ResourceAttributes.Verb {
						case "list", "delete":
							return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
						default:
							return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: false}}, nil
						}
					},
				},
			},
			expectedResponse: &corev1.GetPackageRepositoryPermissionsResponse{
				Permissions: []*corev1.PackageRepositoriesPermissions{
					{
						Plugin: GetPluginDetail(),
						Global: map[string]bool{
							"create": true,
							"delete": true,
							"get":    true,
							"list":   true,
							"update": true,
							"watch":  true,
						},
						Namespace: map[string]bool{
							"create": false,
							"delete": true,
							"get":    false,
							"list":   true,
							"update": false,
							"watch":  false,
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			typedClient := typfake.NewSimpleClientset()
			for _, reaction := range tc.reactors {
				typedClient.PrependReactor(reaction.verb, reaction.resource, reaction.reaction)
			}

			s := Server{
				pluginConfig: &kappControllerPluginParsedConfig{
					globalPackagingNamespace: fallbackGlobalPackagingNamespace,
				},
				clientGetter: clientgetter.NewBuilder().
					WithTyped(typedClient).
					Build(),
				globalPackagingCluster: defaultGlobalContext.Cluster,
			}

			response, err := s.GetPackageRepositoryPermissions(context.Background(), connect.NewRequest(tc.request))

			if got, want := connect.CodeOf(err), tc.expectedErrorCode; err != nil && got != want {
				t.Fatalf("got: %+v, want: %+v, err: %+v", got, want, err)
			}

			// We don't need to check anything else for non-OK codes.
			if tc.expectedErrorCode != 0 {
				return
			}

			if got, want := response.Msg, tc.expectedResponse; !cmp.Equal(want, got, ignoreUnexported) {
				t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got, ignoreUnexported))
			}
		})
	}
}

// Implementing a FakeDepsFactoryImpl for injecting the typed and dynamic k8s clients
type FakeDepsFactoryImpl struct {
	kappcmdcore.DepsFactoryImpl
	coreClient    kubernetes.Interface
	dynamicClient dynamic.Interface

	configFactory   kappcmdcore.ConfigFactory
	ui              ui.UI
	printTargetOnce *sync.Once
	Warnings        bool
}

var _ kappcmdcore.DepsFactory = &FakeDepsFactoryImpl{}

func NewFakeDepsFactoryImpl() *FakeDepsFactoryImpl {
	return &FakeDepsFactoryImpl{
		configFactory:   &ConfigurableConfigFactoryImpl{},
		ui:              ui.NewNoopUI(),
		printTargetOnce: &sync.Once{},
	}
}

func (f *FakeDepsFactoryImpl) SetCoreClient(coreClient kubernetes.Interface) {
	f.coreClient = coreClient
}

func (f *FakeDepsFactoryImpl) SetDynamicClient(dynamicClient dynamic.Interface) {
	f.dynamicClient = dynamicClient
}

func (f *FakeDepsFactoryImpl) CoreClient() (kubernetes.Interface, error) {
	return f.coreClient, nil
}

func (f *FakeDepsFactoryImpl) DynamicClient(opts kappcmdcore.DynamicClientOpts) (dynamic.Interface, error) {
	return f.dynamicClient, nil
}
