const element = document.querySelector("[data-go-back]");
if (element && element.tagName == "A") {
    element.addEventListener("click", function(event) {
        if (document.referrer) {
            history.back();
            event.preventDefault();
        }
    });
}
