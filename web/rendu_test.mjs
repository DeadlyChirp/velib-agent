// Tests du rendu front, sans aucune dépendance ni étape de build.
//
//     node web/rendu_test.mjs
//
// POURQUOI CE FICHIER EXISTE. Le texte affiché vient d'un modèle de langage,
// donc d'une source qu'on ne contrôle pas. Trois fonctions le manipulent avant
// qu'il atteigne l'écran, et une seule erreur — un innerHTML au lieu d'un
// textContent — transformerait une réponse en vecteur d'injection.
//
// COMMENT, SANS OUTILLAGE. Le projet n'a ni node_modules ni bundler, et ce
// n'est pas un oubli : c'est ce qui permet à l'image finale d'être un nginx qui
// sert un fichier statique. Alors plutôt qu'installer un moteur de DOM, on en
// écrit une doublure de trente lignes qui n'implémente QUE ce que le code
// utilise. Si le code se met un jour à toucher innerHTML ou insertAdjacentHTML,
// la doublure ne les connaît pas et le test casse — ce qui est exactement le
// signal qu'on veut.
//
// Les fonctions sont extraites de index.html plutôt que dupliquées ici : un
// test qui vérifie une copie du code ne vérifie rien.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const ici = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(join(ici, "index.html"), "utf8");

// ── Doublure de DOM ─────────────────────────────────────────────────────────
//
// Volontairement minimale. Elle n'expose PAS innerHTML : toute tentative d'en
// poser un passerait inaperçue en JavaScript, donc on vérifie séparément que le
// mot n'apparaît pas dans le code (voir le dernier test).

class Noeud {
  constructor(nom) {
    this.localName = nom;
    this.enfants = [];
    this._texte = "";
    this.dataset = {};
    this.className = "";
  }
  set textContent(v) {
    this._texte = String(v);
    this.enfants = [];
  }
  get textContent() {
    return this.enfants.length
      ? this.enfants.map((e) => e.textContent).join("")
      : this._texte;
  }
  appendChild(n) {
    this.enfants.push(n);
    return n;
  }
  // Le vrai code lit childNodes pour savoir s'il a produit quelque chose.
  get childNodes() {
    return this.enfants;
  }
  append(...ns) {
    ns.forEach((n) => this.appendChild(n));
  }
  // Rend l'arbre sous une forme comparable : « p(strong(Jean),  76 vélos) ».
  arbre() {
    if (this.localName === "#text") return JSON.stringify(this._texte);
    const dedans = this.enfants.map((e) => e.arbre()).join(", ");
    return this.enfants.length
      ? `${this.localName}(${dedans})`
      : `${this.localName}[${JSON.stringify(this._texte)}]`;
  }
}

globalThis.document = {
  createElement: (nom) => new Noeud(nom),
  createTextNode: (t) => {
    const n = new Noeud("#text");
    n._texte = String(t);
    return n;
  },
};

// ── Extraction des fonctions testées ────────────────────────────────────────

function extraire(nom) {
  const debut = source.indexOf(`function ${nom}(`);
  if (debut < 0) throw new Error(`fonction ${nom} introuvable dans index.html`);
  // On lit jusqu'à l'accolade fermante de même niveau.
  let profondeur = 0;
  let i = source.indexOf("{", debut);
  const ouverture = i;
  for (; i < source.length; i++) {
    if (source[i] === "{") profondeur++;
    else if (source[i] === "}") {
      profondeur--;
      if (profondeur === 0) break;
    }
  }
  return source.slice(debut, i + 1);
}

const FINE = " ";
const NBSP = " ";
const contexte = {};
const code =
  `const FINE = ${JSON.stringify(FINE)}; const NBSP = ${JSON.stringify(NBSP)};\n` +
  extraire("typographieFR") + "\n" +
  extraire("remplirAvecEmphase") + "\n" +
  extraire("rendreReponse") + "\n" +
  "Object.assign(cible, { typographieFR, remplirAvecEmphase, rendreReponse });";
new Function("cible", code)(contexte);
const { typographieFR, rendreReponse } = contexte;

// ── Cadre de test ───────────────────────────────────────────────────────────

let passes = 0;
const echecs = [];

function verifie(nom, obtenu, attendu) {
  if (obtenu === attendu) {
    passes++;
  } else {
    echecs.push(`${nom}\n     obtenu : ${obtenu}\n     attendu : ${attendu}`);
  }
}

function affirme(nom, condition, detail) {
  if (condition) passes++;
  else echecs.push(`${nom}\n     ${detail}`);
}

// ── Typographie française ───────────────────────────────────────────────────
//
// C'est le détail que remarque un lecteur français en trois secondes, et
// qu'aucune interface générée ne fait. Il vaut d'être protégé.

verifie(
  "milliers : 1519 devient 1 519 avec une espace fine",
  typographieFR("1519 stations"),
  `1${FINE}519 stations`
);

verifie(
  "ponctuation haute : espace fine avant le point d'interrogation",
  typographieFR("Combien de stations ?"),
  `Combien de stations${FINE}?`
);

verifie(
  "une espace deja presente est absorbee, pas doublee",
  typographieFR("Combien ?"),
  `Combien${FINE}?`
);

verifie(
  "les grands nombres sont groupes par trois",
  typographieFR("1519000 velos"),
  `1${FINE}519${FINE}000 velos`
);

verifie(
  "un nombre de trois chiffres n'est pas touche",
  typographieFR("22 stations"),
  "22 stations"
);

// Une année n'est PAS une quantité. « en 2026 » ne doit jamais devenir
// « en 2 026 » : c'est la limite connue d'un découpage purement numérique, et
// le test la fige pour qu'elle soit un choix et non une surprise.
affirme(
  "limite connue : une annee est groupee comme un nombre",
  typographieFR("en 2026").includes(FINE),
  "si ce test casse, c'est que le groupement distingue desormais les annees — " +
    "mettre a jour ce commentaire"
);

verifie(
  "guillemets francais colles a leur contenu",
  typographieFR("« Chatelet »"),
  `«${NBSP}Chatelet${NBSP}»`
);

// ── Rendu des réponses ──────────────────────────────────────────────────────

{
  const hote = new Noeud("div");
  rendreReponse(hote, "Il y a 1519 stations.");
  // Le paragraphe contient un NOEUD texte, pose par remplirAvecEmphase, et non
  // un textContent affecte directement : d'ou les parentheses et non les
  // crochets dans la representation.
  verifie(
    "une reponse simple devient un paragraphe",
    hote.arbre(),
    `div(p(${JSON.stringify(`Il y a 1${FINE}519 stations.`)}))`
  );
}

{
  const hote = new Noeud("div");
  rendreReponse(hote, "1. Alpha - 76 velos\n2. Beta - 63 velos");
  affirme(
    "une liste numerotee devient un <ol> de deux <li>",
    hote.enfants.length === 1 &&
      hote.enfants[0].localName === "ol" &&
      hote.enfants[0].enfants.length === 2,
    `obtenu : ${hote.arbre()}`
  );
}

{
  const hote = new Noeud("div");
  rendreReponse(hote, "- Alpha\n- Beta\n- Gamma");
  affirme(
    "une liste a puces devient un <ul> de trois <li>",
    hote.enfants[0]?.localName === "ul" && hote.enfants[0].enfants.length === 3,
    `obtenu : ${hote.arbre()}`
  );
}

{
  const hote = new Noeud("div");
  rendreReponse(hote, "**Jean Mace** : 76 velos");
  affirme(
    "le gras Markdown devient un <strong>",
    hote.arbre().includes("strong"),
    `obtenu : ${hote.arbre()}`
  );
}

{
  const hote = new Noeud("div");
  rendreReponse(hote, "");
  affirme(
    "une reponse vide ne produit rien plutot qu'un paragraphe fantome",
    hote.enfants.length === 0,
    `obtenu : ${hote.arbre()}`
  );
}

// ── Sûreté : la sortie du modèle ne devient JAMAIS du balisage ──────────────
//
// Le test qui compte le plus. Un modèle peut produire n'importe quoi, et une
// source distante compromise pourrait le pousser à produire exactement ceci.

const charges = [
  "<script>alert(1)</script>",
  '<img src=x onerror="alert(1)">',
  "<iframe src='javascript:alert(1)'></iframe>",
  "</div><script>fetch('//evil')</script>",
  "<svg/onload=alert(1)>",
  "javascript:alert(document.cookie)",
];

for (const charge of charges) {
  const hote = new Noeud("div");
  rendreReponse(hote, `La station ${charge} a 5 velos.`);

  const balises = [];
  (function parcours(n) {
    if (n.localName !== "#text") balises.push(n.localName);
    n.enfants.forEach(parcours);
  })(hote);

  const suspectes = balises.filter(
    (b) => !["div", "p", "ol", "ul", "li", "strong"].includes(b)
  );
  affirme(
    `injection neutralisee : ${charge.slice(0, 34)}`,
    suspectes.length === 0,
    `elements crees : ${suspectes.join(", ")}`
  );

  // Et la charge doit rester LISIBLE : on la neutralise, on ne la censure pas.
  // Un utilisateur doit pouvoir voir ce que le modele a produit.
  //
  // On compare apres typographie, parce qu'elle modifie legitimement le texte :
  // l'espace fine avant la ponctuation haute transforme « javascript:alert »
  // en « javascript :alert ». Effet de bord heureux — un URI javascript: ainsi
  // espace n'est plus reconnu comme tel par un navigateur — mais c'est un
  // ACCIDENT, pas une defense. La vraie defense reste de ne jamais ecrire de
  // HTML brut, ce que verifie le test suivant.
  const attenduApresTypo = typographieFR(charge).slice(0, 12);
  affirme(
    `la charge reste visible en texte : ${charge.slice(0, 22)}`,
    hote.textContent.includes(attenduApresTypo),
    `texte rendu : ${hote.textContent.slice(0, 80)}`
  );
}

// La preuve structurelle, en complément : le code ne doit contenir AUCUNE
// écriture de HTML brut. C'est ce qui rend l'injection impossible par
// construction plutôt que par vigilance.
for (const interdit of ["innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"]) {
  const lignes = source
    .split("\n")
    .map((l, i) => [i + 1, l])
    .filter(([, l]) => l.includes(interdit) && !l.trimStart().startsWith("//"));
  affirme(
    `aucune ecriture de HTML brut : ${interdit}`,
    lignes.length === 0,
    `trouve ligne(s) ${lignes.map(([n]) => n).join(", ")}`
  );
}

// ── Verdict ─────────────────────────────────────────────────────────────────

console.log(`${passes} verifications passees`);
if (echecs.length) {
  console.log(`\n${echecs.length} ECHEC(S) :\n`);
  echecs.forEach((e) => console.log("  - " + e + "\n"));
  process.exit(1);
}
console.log("rendu front : tout est vert");
