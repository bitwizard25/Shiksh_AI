package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

func (a *API) handleGetMe(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	user, err := a.accounts.Me(r.Context(), userID)
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

// patchMeRequest: absent and null fields both mean "leave unchanged".
type patchMeRequest struct {
	DisplayName   *string `json:"display_name"`
	PreferredLang *string `json:"preferred_lang"`
	Grade         *int    `json:"grade"`
}

func (a *API) handlePatchMe(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var req patchMeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := a.accounts.UpdateProfile(r.Context(), userID, usecase.ProfileInput{
		DisplayName: req.DisplayName, PreferredLang: req.PreferredLang, Grade: req.Grade,
	})
	if err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

type deleteMeRequest struct {
	Password string `json:"password"`
}

func (a *API) handleDeleteMe(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var req deleteMeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.accounts.DeleteAccount(r.Context(), userID, req.Password); err != nil {
		serviceError(a.log, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type languageResponse struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	NativeName string `json:"native_name"`
}

func (a *API) handleLanguages(w http.ResponseWriter, _ *http.Request) {
	langs := usecase.Languages()
	out := make([]languageResponse, 0, len(langs))
	for _, l := range langs {
		out = append(out, languageResponse{Code: l.Code, Name: l.Name, NativeName: l.NativeName})
	}
	writeJSON(w, http.StatusOK, out)
}
