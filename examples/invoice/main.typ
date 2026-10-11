#set page(paper: "a4", margin: 24mm)
#set text(font: "Fira Sans", size: 11pt)
#let invoice = json("/data.json")
#image("/assets/logo.svg", width: 16mm)
= Invoice #invoice.number
Customer: #invoice.customer
#include "sections/details.typ"
