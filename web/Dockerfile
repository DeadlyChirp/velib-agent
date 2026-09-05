# Le front est statique : pas d'étape de build, pas de node_modules. L'image se
# résume à nginx plus trois fichiers.
#
# ⚠️ Les fichiers sont copiés UN PAR UN, ce qui oblige à penser à cette ligne en
# ajoutant un fichier. C'est délibéré — un COPY . emporterait aussi le test
# rendu_test.mjs, qui n'a rien à faire dans une image de production.
#
# Le piège s'est produit : en extrayant typographie.js des deux pages, j'ai
# d'abord oublié de l'ajouter ici. Les pages se seraient chargées, le script
# aurait renvoyé 404, et typographieFR aurait été indéfini — un front cassé au
# premier affichage, sans que rien n'échoue à la construction.
FROM nginx:1.29-alpine

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html      /usr/share/nginx/html/index.html
COPY tracker.html    /usr/share/nginx/html/tracker.html
COPY typographie.js  /usr/share/nginx/html/typographie.js

EXPOSE 80
