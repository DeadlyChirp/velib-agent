package httpapi

import "net/http"

// handleMetrics expose l'état interne du service.
//
// Route de diagnostic destinée à l'exploitation, pas à l'utilisateur final.
// Elle ne contient AUCUN contenu de message : uniquement des compteurs, des
// latences et des identifiants de conversation. Un tableau de bord qui exposerait
// les questions des utilisateurs serait une fuite de données déguisée en outil.
//
// Dans un déploiement réel, elle serait derrière une authentification ou
// restreinte au réseau interne. Ici elle est ouverte, et c'est assumé : c'est
// aussi ce qui permet de la montrer pendant la soutenance.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.metrics.Snapshot())
}
