# 本地二维码编码器

`qrcode.mjs` 是 node-qrcode 1.5.4 的浏览器 ES module bundle，包含 dijkstrajs 1.0.3，用于在当前浏览器本地生成 Native 付款二维码，无 CDN 或外部图片服务。原始 MIT 许可证见同目录两个 LICENSE 文件。

源代码：<https://github.com/soldair/node-qrcode/tree/v1.5.4>；<https://github.com/andrewhayward/dijkstra>。

生成工具：esbuild 0.28.2。当前使用工作区已有的相同版本包离线生成；服务运行和部署不需要 Node 或这些构建依赖。需要更新时，在独立临时目录安装固定版本并重新生成：

```sh
npm install --prefix /tmp/pay-qr-build qrcode@1.5.4 dijkstrajs@1.0.3 esbuild@0.28.2
/tmp/pay-qr-build/node_modules/.bin/esbuild \
  /tmp/pay-qr-build/node_modules/qrcode/lib/browser.js \
  --bundle --minify --format=esm --platform=browser --target=es2020 \
  --outfile=web/vendor/qrcode.mjs
```

升级时同时保留依赖许可证，并运行浏览器微信 Native 场景回归。
