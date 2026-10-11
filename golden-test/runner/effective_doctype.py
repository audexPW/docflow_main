ACT_SOURCE_TYPES = frozenset({
    "act",
    "schet_faktura",
    "act_priemki",
    "invoice",
    "receipt",
    "payment_order",
    "act_sverki",
})


def effective_doc_type(source):
    if isinstance(source, str) and source in ACT_SOURCE_TYPES:
        return "act"
    return source
