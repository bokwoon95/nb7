const elements = document.querySelectorAll("[data-dismiss-alert]");
for (const element of elements) {
    element.addEventListener("click", function() {
        let parentElement = element.parentElement;
        while (parentElement != null) {
            const role = parentElement.getAttribute("role");
            if (role !== "alert") {
                parentElement = parentElement.parentElement;
                continue;
            }
            parentElement.style.transition = "opacity 100ms linear";
            parentElement.style.opacity = "0";
            setTimeout(function() {
                parentElement.style.display = "none";
            }, 100);
            return;
        }
    });
}
