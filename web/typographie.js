// Typographie française — source UNIQUE, partagée par les deux pages.
//
// ── POURQUOI CE FICHIER EXISTE ──────────────────────────────────────────────
//
// La règle a d'abord été écrite dans index.html, puis recopiée dans
// tracker.html pour que les deux pages écrivent « 1 519 » de la même façon.
// Deux copies de la même règle divergent toujours, et celles-ci avaient déjà
// commencé — mesuré :
//
//     entrée            index.html            tracker.html
//     « Châtelet »      «·Châtelet·»          « Châtelet »
//
// La copie du tracker n'avait jamais reçu la règle des guillemets, et
// appliquait les deux autres dans l'ordre inverse. Deux pages qui affichent le
// même chiffre doivent l'écrire pareil : c'était tout l'intérêt du travail, et
// la duplication l'annulait en silence.
//
// ── POURQUOI UN FICHIER SÉPARÉ ET PAS UN BUNDLER ────────────────────────────
//
// Le projet n'a ni node_modules ni étape de build, et c'est ce qui permet à
// l'image finale d'être un nginx qui sert des fichiers statiques. Un <script
// src> est du HTML de 1995 : deux pages, un fichier, zéro outillage.
//
// ── LA RÈGLE ────────────────────────────────────────────────────────────────
//
// En français, la ponctuation haute — ? ! ; : — est précédée d'une espace fine
// insécable, et les milliers en sont séparés. « 1 519 stations ? », jamais
// « 1519 stations? ». Un lecteur français voit la différence en trois secondes,
// et aucun modèle de langage ne la produit spontanément.
//
// Appliqué à L'AFFICHAGE seulement. La valeur envoyée à l'API et celle stockée
// restent le texte brut : on ne réécrit jamais la donnée pour des raisons de
// mise en forme.

const FINE = " "; // espace fine insécable
const NBSP = " "; // espace insécable

// Les trois formes d'espace qu'on peut rencontrer avant une ponctuation :
// l'espace ordinaire, l'insécable et la fine. On les absorbe pour ne jamais en
// empiler deux.
const ESPACES = "[   ]";

function typographieFR(s) {
  return (
    String(s)
      // Espace fine avant la ponctuation haute, en absorbant celle déjà là.
      .replace(new RegExp(ESPACES + "*([?!;:])", "g"), FINE + "$1")
      // Séparateur de milliers sur les entiers nus de 4 à 9 chiffres.
      //
      // Borné volontairement : au-delà de neuf chiffres on est sur un
      // identifiant et non sur une quantité, et l'espacer le rendrait
      // illisible. Limite connue et assumée : une année passe dans le filet,
      // « en 2026 » devient « en 2 026 ». Distinguer les deux demanderait de
      // comprendre la phrase, ce qui n'est pas le rôle d'un formateur.
      .replace(/\b\d{4,9}\b/g, (m) => m.replace(/\B(?=(\d{3})+$)/g, FINE))
      // Guillemets français collés à leur contenu : « comme ceci ».
      .replace(new RegExp("«" + ESPACES + "*", "g"), "«" + NBSP)
      .replace(new RegExp(ESPACES + "*»", "g"), NBSP + "»")
  );
}

// Exporté pour le test Node. Dans le navigateur, la fonction est simplement
// globale : les deux pages la chargent avant leur propre script.
if (typeof module !== "undefined" && module.exports) {
  module.exports = { typographieFR, FINE, NBSP };
}
