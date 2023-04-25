package frontend

import (
	"context"
	"github.com/Azure/ARO-RP/pkg/api"
	"github.com/Azure/ARO-RP/pkg/database/cosmosdb"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/Azure/ARO-RP/pkg/frontend/middleware"
)

func (f *frontend) postAdminOpenShiftClusterEtcdRecovery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := ctx.Value(middleware.ContextKeyLog).(*logrus.Entry)
	r.URL.Path = filepath.Dir(r.URL.Path)

	err := f._postAdminOpenShiftClusterEtcdRecovery(ctx, r, log)

	adminReply(log, w, nil, nil, err)
}

func (f *frontend) _postAdminOpenShiftClusterEtcdRecovery(ctx context.Context, r *http.Request, log *logrus.Entry) error {
	var err error
	resType, resName, resGroupName := chi.URLParam(r, "resourceType"), chi.URLParam(r, "resourceName"), chi.URLParam(r, "resourceGroupName")

	groupKind, namespace, name := r.URL.Query().Get("kind"), r.URL.Query().Get("namespace"), r.URL.Query().Get("name")
	//groupKind, namespace := r.URL.Query().Get("kind"), r.URL.Query().Get("namespace")

	//if groupKind != "Etcd" {
	//	return api.NewCloudError(http.StatusUnprocessableEntity, api.CloudErrorCodeForbidden, "", "The Group Kind '%s' is not valid", groupKind)
	//}

	//if namespace != nameSpaceEtcds {
	//	return api.NewCloudError(http.StatusUnprocessableEntity, api.CloudErrorCodeForbidden, "", "The Namespace '%s' is not valid", namespace)
	//}

	resourceID := strings.TrimPrefix(r.URL.Path, "/admin")

	doc, err := f.dbOpenShiftClusters.Get(ctx, resourceID)
	switch {
	case cosmosdb.IsErrorStatusCode(err, http.StatusNotFound):
		return api.NewCloudError(http.StatusNotFound, api.CloudErrorCodeResourceNotFound, "", "The Resource '%s/%s' under resource group '%s' was not found.", resType, resName, resGroupName)
	case err != nil:
		return err
	}
	kubeActions, err := f.kubeActionsFactory(log, f.env, doc.OpenShiftCluster)
	if err != nil {
		return err
	}

	gvr, err := kubeActions.ResolveGVR(groupKind)
	if err != nil {
		return err
	}

	err = validateAdminKubernetesObjects(r.Method, gvr, namespace, name)
	if err != nil {
		return err
	}

	return f.fixEtcd(ctx, log, f.env, doc, kubeActions, name, namespace, groupKind)
}
