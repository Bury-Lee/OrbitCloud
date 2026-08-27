# web 镜像: Nginx 托管前端 + /api 反代
FROM nginx:alpine

COPY web/* /usr/share/nginx/html/
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80
