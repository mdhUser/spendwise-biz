package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/mdhuser/spendwise-biz/internal/authclient"
	catalogapp "github.com/mdhuser/spendwise-biz/internal/catalog/app"
	"github.com/mdhuser/spendwise-biz/internal/platform/httpx"
)

type Handler struct {
	service *catalogapp.Service
}

func New(service *catalogapp.Service) *Handler { return &Handler{service: service} }

func (h *Handler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/catalog/categories", auth(http.HandlerFunc(h.listCategories)))
	mux.Handle("POST /v1/catalog/categories", auth(http.HandlerFunc(h.createCategory)))
	mux.Handle("PATCH /v1/catalog/categories/{id}", auth(http.HandlerFunc(h.updateCategory)))
	mux.Handle("DELETE /v1/catalog/categories/{id}", auth(http.HandlerFunc(h.deleteCategory)))

	mux.Handle("GET /v1/catalog/products", auth(http.HandlerFunc(h.listProducts)))
	mux.Handle("POST /v1/catalog/products", auth(http.HandlerFunc(h.createProduct)))
	mux.Handle("GET /v1/catalog/products/{id}", auth(http.HandlerFunc(h.getProduct)))
	mux.Handle("PATCH /v1/catalog/products/{id}", auth(http.HandlerFunc(h.updateProduct)))
	mux.Handle("DELETE /v1/catalog/products/{id}", auth(http.HandlerFunc(h.deleteProduct)))

	mux.Handle("GET /v1/catalog/products/{id}/skus", auth(http.HandlerFunc(h.listSKUs)))
	mux.Handle("POST /v1/catalog/products/{id}/skus", auth(http.HandlerFunc(h.createSKU)))
	mux.Handle("PATCH /v1/catalog/skus/{id}", auth(http.HandlerFunc(h.updateSKU)))
	mux.Handle("DELETE /v1/catalog/skus/{id}", auth(http.HandlerFunc(h.deleteSKU)))
	mux.Handle("POST /v1/catalog/skus/{id}/quotes", auth(http.HandlerFunc(h.createQuote)))

	mux.Handle("GET /v1/library", auth(http.HandlerFunc(h.library)))
	mux.Handle("GET /v1/library/items", auth(http.HandlerFunc(h.listItems)))
	mux.Handle("POST /v1/library/items", auth(http.HandlerFunc(h.createItem)))
	mux.Handle("DELETE /v1/library/items/{id}", auth(http.HandlerFunc(h.deleteItem)))
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	items, err := h.service.ListCategories(r.Context(), owner)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "categories": items})
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.CategoryInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateCategory(r.Context(), owner, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, "/v1/catalog/categories/"+body.ID)
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.UpdateCategoryInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.UpdateCategory(r.Context(), owner, r.PathValue("id"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	if err := h.service.DeleteCategory(r.Context(), owner, r.PathValue("id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	body, err := h.service.ListProducts(r.Context(), owner, r.URL.Query().Get("cursor"), r.URL.Query().Get("categoryId"), limitQuery(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) getProduct(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	body, err := h.service.GetProduct(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.ProductInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateProduct(r.Context(), owner, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, "/v1/catalog/products/"+body.ID)
}

func (h *Handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.UpdateProductInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.UpdateProduct(r.Context(), owner, r.PathValue("id"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) deleteProduct(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	if err := h.service.DeleteProduct(r.Context(), owner, r.PathValue("id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}

func (h *Handler) listSKUs(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	items, err := h.service.ListSKUs(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createSKU(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.SKUInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateSKU(r.Context(), owner, r.PathValue("id"), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, "/v1/catalog/skus/"+body.ID)
}

func (h *Handler) updateSKU(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.UpdateSKUInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.UpdateSKU(r.Context(), owner, r.PathValue("id"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) deleteSKU(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	if err := h.service.DeleteSKU(r.Context(), owner, r.PathValue("id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}

func (h *Handler) createQuote(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.QuoteInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateQuote(r.Context(), owner, r.PathValue("id"), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, "")
}

func (h *Handler) library(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	body, err := h.service.Library(r.Context(), owner, r.URL.Query().Get("cursor"), limitQuery(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) listItems(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	body, err := h.service.ListItems(r.Context(), owner, r.URL.Query().Get("cursor"), limitQuery(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) createItem(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input catalogapp.ItemInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateItem(r.Context(), owner, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, "/v1/library/items/"+body.ID)
}

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	if err := h.service.DeleteItem(r.Context(), owner, r.PathValue("id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}

func ownerID(r *http.Request) (string, bool) {
	user, ok := authclient.UserFromContext(r.Context())
	if !ok || strings.TrimSpace(user.ID) == "" {
		return "", false
	}
	return user.ID, true
}

func limitQuery(r *http.Request) int {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return 50
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 50
	}
	if parsed > 100 {
		return 100
	}
	return parsed
}

func writeMutation(w http.ResponseWriter, body any, replay bool, location string) {
	if location != "" {
		w.Header().Set("Location", location)
	}
	if replay {
		w.Header().Set("Idempotent-Replay", "true")
		httpx.WriteJSON(w, http.StatusOK, body)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, body)
}
