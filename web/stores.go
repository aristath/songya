package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/aristath/songya"
)

type storeNavigationKey struct{}
type storeNavigation struct {
	Selected string
	Stores   []string
}

type StoreApplication struct {
	http.Handler
	applications map[string]*Application
}

func (a *StoreApplication) Close() error {
	var issues []error
	for _, app := range a.applications {
		issues = append(issues, app.Close())
	}
	return errors.Join(issues...)
}

func HandlerForStores(config songya.ApplicationConfiguration) *StoreApplication {
	application := &StoreApplication{applications: map[string]*Application{}}
	issues := map[string]error{}
	names := []string{}
	for name, directory := range config.Stores {
		names = append(names, name)
		store, err := songya.Open(directory)
		if err != nil {
			issues[name] = err
			continue
		}
		application.applications[name] = Handler(store)
	}
	slices.Sort(names)
	password := sha256.Sum256([]byte(config.Password))
	application.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, supplied, ok := r.BasicAuth()
		candidate := sha256.Sum256([]byte(supplied))
		if !ok || config.Password == "" || subtle.ConstantTimeCompare(password[:], candidate[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Songya", charset="UTF-8"`)
			http.Error(w, "Administrator authentication is required.", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/stores/"+names[0]+"/", http.StatusSeeOther)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			for _, app := range application.applications {
				app.ServeHTTP(w, r)
				return
			}
			http.Error(w, "No readable stores.", http.StatusServiceUnavailable)
			return
		}
		path, found := strings.CutPrefix(r.URL.Path, "/stores/")
		name, suffix, _ := strings.Cut(path, "/")
		if !found {
			http.NotFound(w, r)
			return
		}
		if issue := issues[name]; issue != nil {
			http.Error(w, issue.Error(), http.StatusServiceUnavailable)
			return
		}
		app := application.applications[name]
		if app == nil {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusSeeOther)
			return
		}
		request := r.Clone(context.WithValue(r.Context(), storeNavigationKey{}, storeNavigation{name, names}))
		request.URL.Path = "/" + suffix
		request.URL.RawPath = ""
		app.ServeHTTP(w, request)
	})
	return application
}
