// Package proposer contains APIs for the proposer as per builder-specs
package proposer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/flashbots/boost-relay/common"
	"github.com/flashbots/boost-relay/datastore"
	"github.com/flashbots/go-boost-utils/types"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

var (
	pathRegisterValidator = "/eth/v1/builder/validators"
	pathGetHeader         = "/eth/v1/builder/header/{slot:[0-9]+}/{parent_hash:0x[a-fA-F0-9]+}/{pubkey:0x[a-fA-F0-9]+}"
	pathGetPayload        = "/eth/v1/builder/blinded_blocks"
)

type ProposerAPI struct {
	common.BaseAPI

	ctx                  context.Context
	datastore            datastore.ProposerDatastore
	builderSigningDomain types.Domain
}

func NewProposerAPI(
	ctx context.Context,
	log *logrus.Entry,
	ds datastore.ProposerDatastore,
	genesisForkVersionHex string,
) (ret common.APIComponent, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if log == nil {
		return nil, errors.New("log parameter is nil")
	}

	if ds == nil {
		return nil, errors.New("proposer API datastore parameter is nil")
	}

	api := &ProposerAPI{
		ctx:       ctx,
		datastore: ds,
	}

	// Setup the remaining fields
	api.Log = log.WithField("module", "api/proposer")
	api.builderSigningDomain, err = common.ComputerBuilderSigningDomain(genesisForkVersionHex)
	return api, err
}

func (api *ProposerAPI) RegisterHandlers(r *mux.Router) {
	r.HandleFunc(pathRegisterValidator, api.handleRegisterValidator).Methods(http.MethodPost)
	r.HandleFunc(pathGetHeader, api.handleGetHeader).Methods(http.MethodGet)
	r.HandleFunc(pathGetPayload, api.handleGetPayload).Methods(http.MethodPost)
}

func (api *ProposerAPI) Start() (err error) {
	cnt, err := api.datastore.RefreshKnownValidators()
	if err != nil {
		return err
	}

	if cnt == 0 {
		api.Log.WithField("cnt", cnt).Warn("updated known validators, but have not received any")
	} else {
		api.Log.WithField("cnt", cnt).Info("updated known validators")
	}

	// Start periodic updates of known validators
	go func() {
		select {
		case <-api.ctx.Done():
			return
		case <-time.NewTicker(common.DurationPerEpoch).C:
			cnt, err = api.datastore.RefreshKnownValidators()
			if err != nil {
				api.Log.WithError(err).Error("error getting known validators")
			} else {
				if cnt == 0 {
					api.Log.WithField("cnt", cnt).Warn("updated known validators, but have not received any")
				} else {
					api.Log.WithField("cnt", cnt).Info("updated known validators")
				}
			}
		}
	}()
	return nil
}

func (api *ProposerAPI) Stop() error {
	api.ctx.Done()
	return nil
}

func (api *ProposerAPI) handleRegisterValidator(w http.ResponseWriter, req *http.Request) {
	log := api.Log.WithField("method", "registerValidator")
	log.Info("registerValidator")

	payload := []types.SignedValidatorRegistration{}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		api.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	for _, registration := range payload {
		if len(registration.Message.Pubkey) != 48 {
			continue
		}

		if len(registration.Signature) != 96 {
			continue
		}

		// Check if actually a real validator
		isKnownValidator := api.datastore.IsKnownValidator(types.NewPubkeyHex(registration.Message.Pubkey.String()))
		if !isKnownValidator {
			log.WithField("registration", fmt.Sprintf("%+v", registration)).Warn("not a known validator")
			continue
		}

		// Verify the signature
		ok, err := types.VerifySignature(registration.Message, api.builderSigningDomain, registration.Message.Pubkey[:], registration.Signature[:])
		if err != nil || !ok {
			log.WithError(err).WithField("registration", fmt.Sprintf("%+v", registration)).Warn("failed to verify registerValidator signature")
			continue
		}

		// Save or update (if newer timestamp than previous registration)
		err = api.datastore.UpdateValidatorRegistration(registration)
		if err != nil {
			log.WithError(err).WithField("registration", fmt.Sprintf("%+v", registration)).Error("error updating validator registration")
			continue
		}
	}

	api.RespondOK(w, common.NilResponse)
}

func (api *ProposerAPI) handleGetHeader(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	slot := vars["slot"]
	parentHashHex := vars["parent_hash"]
	pubkey := vars["pubkey"]
	log := api.Log.WithFields(logrus.Fields{
		"method":     "getHeader",
		"slot":       slot,
		"parentHash": parentHashHex,
		"pubkey":     pubkey,
	})
	log.Info("getHeader")

	if _, err := strconv.ParseUint(slot, 10, 64); err != nil {
		api.RespondError(w, http.StatusBadRequest, common.ErrInvalidSlot.Error())
		return
	}

	if len(pubkey) != 98 {
		api.RespondError(w, http.StatusBadRequest, common.ErrInvalidPubkey.Error())
		return
	}

	if len(parentHashHex) != 66 {
		api.RespondError(w, http.StatusBadRequest, common.ErrInvalidHash.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNoContent)
	if err := json.NewEncoder(w).Encode(common.NilResponse); err != nil {
		api.Log.WithError(err).Error("Couldn't write getHeader response")
		http.Error(w, "", http.StatusInternalServerError)
	}
}

func (api *ProposerAPI) handleGetPayload(w http.ResponseWriter, req *http.Request) {
	log := api.Log.WithField("method", "getPayload")
	log.Info("getPayload")

	payload := new(types.SignedBlindedBeaconBlock)
	if err := json.NewDecoder(req.Body).Decode(payload); err != nil {
		api.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if len(payload.Signature) != 96 {
		api.RespondError(w, http.StatusBadRequest, common.ErrInvalidSignature.Error())
		return
	}

	api.RespondOKEmpty(w)
}
