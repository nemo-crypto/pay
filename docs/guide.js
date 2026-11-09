for (const table of document.querySelectorAll("article table")) {
  const wrapper = document.createElement("div");
  wrapper.className = "table-scroll";
  table.before(wrapper);
  wrapper.append(table);
}
for (const heading of document.querySelectorAll("article h2")) {
  if (!heading.id) continue;
  const a = document.createElement("a");
  a.href = "#" + heading.id;
  a.textContent = heading.textContent;
  document.querySelector("#toc").append(a);
}
for (const pre of document.querySelectorAll("article pre")) {
  const button = document.createElement("button");
  button.className = "copy";
  button.textContent = "复制代码";
  button.type = "button";
  button.onclick = async () => {
    try {
      await navigator.clipboard.writeText(
        pre.querySelector("code").textContent,
      );
      button.textContent = "已复制";
      document.querySelector("#copy-status").textContent = "代码已复制";
      setTimeout(() => {
        button.textContent = "复制代码";
        document.querySelector("#copy-status").textContent = "";
      }, 2200);
    } catch {
      document.querySelector("#copy-status").textContent =
        "复制不可用，请选择代码手动复制";
    }
  };
  pre.append(button);
}
