export function exportCardKeys(keys: Array<string | number>) {
  const url = URL.createObjectURL(
    new Blob([keys.map(String).join("\n")], {
      type: "text/plain;charset=utf-8",
    })
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = "card-keys.txt";
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
