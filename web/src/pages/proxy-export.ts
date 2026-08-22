export function exportProxyURLs(urls: string[]) {
  const objectURL = URL.createObjectURL(
    new Blob([urls.join("\n")], { type: "text/plain;charset=utf-8" })
  );
  const link = document.createElement("a");
  link.href = objectURL;
  link.download = "proxies.txt";
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(objectURL);
}
