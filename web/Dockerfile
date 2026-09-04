# Le front est un fichier statique : pas d'étape de build, pas de node_modules.
# L'image se résume à nginx plus deux fichiers.
FROM nginx:1.29-alpine

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html   /usr/share/nginx/html/index.html
COPY tracker.html /usr/share/nginx/html/tracker.html

EXPOSE 80
