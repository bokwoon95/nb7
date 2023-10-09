// https://til.simonwillison.net/javascript/dropdown-menu-with-details-summary
document.body.parentElement.addEventListener('click', (ev) => {
    /* Close any open details elements that this click is outside of */
    var target = ev.target;
    var detailsClickedWithin = null;
    while (target && target.tagName !== 'DETAILS') {
        target = target.parentNode;
    }
    if (target && target.tagName === 'DETAILS') {
        detailsClickedWithin = target;
    }
    Array.from(document.getElementsByTagName('details')).filter(
        (details) => details.open && details !== detailsClickedWithin && !details.hasAttribute("data-dont-autoclose-details")
    ).forEach(details => details.open = false);
});
for (const element of document.querySelectorAll("[data-dismiss-alert]")) {
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
const element = document.querySelector("[data-go-back]");
if (element && element.tagName == "A") {
    element.addEventListener("click", function(event) {
        if (document.referrer) {
            history.back();
            event.preventDefault();
        }
    });
}
for (const element of document.querySelectorAll("[data-disable-click-selection]")) {
    element.addEventListener("mousedown", function(event) {
        // https://stackoverflow.com/a/43321596
        if (event.detail > 1) {
            event.preventDefault();
        }
    });
}
for (const form of document.querySelectorAll("form[data-disable-after-submit]")) {
    form.addEventListener("submit", function() {
        for (const button of form.querySelectorAll("button[type=submit]")) {
            button.disabled = true;
        }
        const element = form.querySelector("[data-loading-spinner]");
        if (element) {
            element.innerHTML = `<div class="mr2">Loading</div><svg width="24" height="24" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg"><style>.spinner_ajPY{transform-origin:center;animation:spinner_AtaB .75s infinite linear}@keyframes spinner_AtaB{100%{transform:rotate(360deg)}}</style><path d="M12,1A11,11,0,1,0,23,12,11,11,0,0,0,12,1Zm0,19a8,8,0,1,1,8-8A8,8,0,0,1,12,20Z" opacity=".25"/><path d="M10.14,1.16a11,11,0,0,0-9,8.92A1.59,1.59,0,0,0,2.46,12,1.52,1.52,0,0,0,4.11,10.7a8,8,0,0,1,6.66-6.61A1.42,1.42,0,0,0,12,2.69h0A1.57,1.57,0,0,0,10.14,1.16Z" class="spinner_ajPY"/></svg>`;
        }
    })
}

const urlSearchParams = (new URL(document.location)).searchParams;

let sort = urlSearchParams.get("sort");
if (sort) {
    sort = sort.trim().toLowerCase();
}
const isDefaultSort = sort === "name" || sort === "created";
if (isDefaultSort) {
    document.cookie = `sort=0; Path=${location.pathname}; Max-Age=-1; SameSite=Lax;`;
} else if (sort === "name" || sort === "created" || sort === "edited" || sort === "title") {
    document.cookie = `sort=${sort}; Path=${location.pathname}; Max-Age=${60 * 60 * 24 * 365}; SameSite=Lax;`;
}

let order = urlSearchParams.get("order");
if (order) {
    order = order.trim().toLowerCase();
}
const isDefaultOrder = order === null || ((sort === "title" || sort === "name") && order === "asc") || ((sort === "created" || sort === "edited") && order === "desc");
if (isDefaultOrder) {
    document.cookie = `order=0; Path=${location.pathname}; Max-Age=-1; SameSite=Lax;`;
} else if (order === "asc" || order === "desc") {
    document.cookie = `order=${order}; Path=${location.pathname}; Max-Age=${60 * 60 * 24 * 365}; SameSite=Lax;`;
}
